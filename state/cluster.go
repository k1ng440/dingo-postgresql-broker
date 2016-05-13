package state

import (
	"fmt"
	"strconv"
	"time"

	"github.com/dingotiles/dingo-postgresql-broker/backend"
	"github.com/dingotiles/dingo-postgresql-broker/broker/structs"
	"github.com/frodenas/brokerapi"
	"github.com/pivotal-golang/lager"
)

// Cluster describes a real/proposed cluster of nodes
type Cluster struct {
	etcdClient backend.EtcdClient
	logger     lager.Logger
	meta       structs.ClusterData
}

func (c *Cluster) MetaData() structs.ClusterData {
	return c.meta
}

// NewCluster creates a RealCluster from ProvisionDetails
func NewClusterFromRestoredData(instanceID string, clusterdata *structs.ClusterData, etcdClient backend.EtcdClient, logger lager.Logger) (cluster *Cluster) {
	cluster = &Cluster{
		etcdClient: etcdClient,
		meta:       *clusterdata,
	}
	if logger != nil {
		cluster.logger = logger.Session("cluster", lager.Data{
			"instance-id": clusterdata.InstanceID,
			"service-id":  clusterdata.ServiceID,
			"plan-id":     clusterdata.PlanID,
		})
	}
	return
}

func (c *Cluster) SetTargetNodeCount(count int) error {
	c.restoreState()
	c.meta.TargetNodeCount = count
	err := c.writeState()
	if err != nil {
		c.logger.Error("set-target-node-count.error", err)
		return err
	}
	return nil
}

func (c *Cluster) writeState() error {
	c.logger.Info("write-state", lager.Data{"meta": c.meta})
	key := fmt.Sprintf("/serviceinstances/%s/plan_id", c.meta.InstanceID)
	_, err := c.etcdClient.Set(key, c.meta.PlanID, 0)
	if err != nil {
		c.logger.Error("write-state.error", err)
		return err
	}
	key = fmt.Sprintf("/serviceinstances/%s/meta", c.meta.InstanceID)
	_, err = c.etcdClient.Set(key, c.meta.Json(), 0)
	fmt.Println("json: %s", c.meta.Json())
	if err != nil {
		c.logger.Error("write-state.error", err)
		return err
	}
	return nil
}

// TODO write ClusterData to etcd
func (c *Cluster) restoreState() error {
	c.logger.Info("restore-state")
	key := fmt.Sprintf("/serviceinstances/%s/meta", c.meta.InstanceID)
	resp, err := c.etcdClient.Get(key, false, false)
	if err != nil {
		c.logger.Error("restore-state.error", err)
		return err
	}
	c.meta = *structs.ClusterDataFromJson(resp.Node.Value)
	return nil
}

func (c *Cluster) PortAllocation() (int64, error) {
	key := fmt.Sprintf("/routing/allocation/%s", c.meta.InstanceID)
	resp, err := c.etcdClient.Get(key, false, false)
	if err != nil {
		c.logger.Error("routing-allocation.get", err)
		return 0, err
	}
	publicPort, err := strconv.ParseInt(resp.Node.Value, 10, 64)
	if err != nil {
		c.logger.Error("bind.routing-allocation.parse-int", err)
		return 0, err
	}
	return publicPort, nil
}

// WaitForRoutingPortAllocation blocks until the routing tier has allocated a public port
func (cluster *Cluster) WaitForRoutingPortAllocation() (err error) {
	for index := 0; index < 10; index++ {
		key := fmt.Sprintf("/routing/allocation/%s", cluster.MetaData().InstanceID)
		resp, err := cluster.etcdClient.Get(key, false, false)
		if err != nil {
			cluster.logger.Debug("provision.routing.polling", lager.Data{})
		} else {
			cluster.meta.AllocatedPort = resp.Node.Value
			cluster.logger.Info("provision.routing.done", lager.Data{"allocated_port": cluster.MetaData().AllocatedPort})
			return nil
		}
		time.Sleep(1 * time.Second)
	}
	cluster.logger.Error("provision.routing.timed-out", err, lager.Data{"err": err})
	return err
}
