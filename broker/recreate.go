package broker

import (
	"fmt"

	"github.com/dingotiles/dingo-postgresql-broker/state"
	"github.com/frodenas/brokerapi"
	"github.com/pivotal-golang/lager"
)

// Provision a new service instance
func (bkr *Broker) Recreate(instanceID string, acceptsIncomplete bool) (resp brokerapi.ProvisioningResponse, async bool, err error) {
	logger := bkr.logger.Session("recreate", lager.Data{
		"instance-id": instanceID,
	})

	logger.Info("start", lager.Data{})
	var clusterdata *state.ClusterData
	err, clusterdata = state.RestoreClusterDataBackup(instanceID, bkr.config.Callbacks, bkr.logger)
	if err != nil {
		err = fmt.Errorf("Cannot recreate service from backup; unable to restore original service instance data: %s", err)
		return
	}

	logger = bkr.logger

	if bkr.state.ClusterExists(instanceID) {
		logger.Info("exists")
		err = fmt.Errorf("Service instance %s still exists in etcd, please clean it out before recreating cluster", instanceID)
		return
	} else {
		logger.Info("not-exists")
	}

	// Restore port allocation from state.MetaData()
	key := fmt.Sprintf("/routing/allocation/%s", instanceID)
	_, err = bkr.etcdClient.Set(key, clusterdata.AllocatedPort, 0)
	if err != nil {
		logger.Error("routing-allocation.error", err)
		return
	}
	logger.Info("routing-allocation.restored", lager.Data{"allocated-port": clusterdata.AllocatedPort})

	targetNodeCount := clusterdata.TargetNodeCount
	if targetNodeCount < 1 {
		targetNodeCount = 1
	}
	clusterdata.TargetNodeCount = 0

	cluster := state.NewClusterFromRestoredData(instanceID, clusterdata, bkr.etcdClient, logger)

	clusterRequest := bkr.scheduler.NewRequest(cluster, targetNodeCount)
	err = bkr.scheduler.Execute(clusterRequest)
	if err != nil {
		logger.Error("provision.perform.error", err)
		return resp, false, err
	}

	// if port is allocated, then wait to confirm containers are running
	err = cluster.WaitForAllRunning()

	if err != nil {
		logger.Error("provision.running.error", err)
		return
	}
	logger.Info("provision.running.success", lager.Data{"cluster": cluster.MetaData()})
	state.TriggerClusterDataBackup(cluster.MetaData(), bkr.config.Callbacks, logger)
	return
}
