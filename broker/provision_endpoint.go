package broker

import (
	"fmt"
	"reflect"

	"github.com/dingotiles/dingo-postgresql-broker/broker/structs"
	"github.com/dingotiles/dingo-postgresql-broker/state"
	"github.com/frodenas/brokerapi"
	"github.com/pivotal-golang/lager"
)

// Provision a new service instance
func (bkr *Broker) Provision(instanceID string, details brokerapi.ProvisionDetails, acceptsIncomplete bool) (resp brokerapi.ProvisioningResponse, async bool, err error) {
	return bkr.provision(structs.ClusterID(instanceID), details, acceptsIncomplete)
}

func (bkr *Broker) provision(instanceID structs.ClusterID, details brokerapi.ProvisionDetails, acceptsIncomplete bool) (resp brokerapi.ProvisioningResponse, async bool, err error) {
	if details.ServiceID == "" && details.PlanID == "" {
		return bkr.Recreate(instanceID, details, acceptsIncomplete)
	}

	logger := bkr.newLoggingSession("provision", lager.Data{"instance-id": instanceID})
	defer logger.Info("done")

	features, err := structs.ClusterFeaturesFromParameters(details.Parameters)
	if err != nil {
		logger.Error("cluster-features", err)
		return resp, false, err
	}

	if err = bkr.assertProvisionPrecondition(instanceID, features); err != nil {
		logger.Error("preconditions.error", err)
		return resp, false, err
	}

	port, err := bkr.router.AllocatePort()
	clusterState := bkr.initCluster(instanceID, port, details)
	clusterModel := state.NewClusterModel(bkr.state, clusterState)

	if bkr.callbacks.Configured() {
		bkr.callbacks.WriteRecreationData(clusterState.RecreationData())
		data, err := bkr.callbacks.RestoreRecreationData(instanceID)
		if !reflect.DeepEqual(clusterState.RecreationData(), data) {
			logger.Error("recreation-data.failure", err)
			return resp, false, err
		}
	}

	// Continue processing in background
	// TODO: if error, store it into etcd; and last_operation_endpoint should look for errors first
	go func() {
		if err := bkr.scheduler.RunCluster(clusterModel, features); err != nil {
			logger.Error("run-cluster", err)
			return
		}

		if err := bkr.router.AssignPortToCluster(instanceID, port); err != nil {
			logger.Error("assign-port", err)
			return
		}

		// If broker has credentials for a Cloud Foundry,
		// attempt to look up service instance to get its user-provided name.
		// This can then be used in future to undo/recreate-from-backup when user
		// only knows the name they provided; and not the internal service instance ID.
		// If operation fails, that's temporarily unfortunate but might be due to credentials
		// not yet having SpaceDeveloper role for the Space being used.
		if bkr.cf != nil && bkr.callbacks.Configured() {
			serviceInstanceName, err := bkr.cf.LookupServiceName(instanceID)
			if err != nil {
				logger.Error("lookup-service-name.error", err,
					lager.Data{"action-required": "Fix issue and run errand/script to update clusterdata backups to include service names"})
			}
			if serviceInstanceName == "" {
				logger.Info("lookup-service-name.not-found")
			} else {
				clusterState.ServiceInstanceName = serviceInstanceName
				bkr.callbacks.WriteRecreationData(clusterState.RecreationData())
				data, err := bkr.callbacks.RestoreRecreationData(instanceID)
				if !reflect.DeepEqual(clusterState.RecreationData(), data) {
					logger.Error("lookup-service-name.update-recreation-data.failure", err)
				} else {
					logger.Info("lookup-service-name.update-recreation-data.saved", lager.Data{"name": serviceInstanceName})
				}
			}
		}
	}()
	return resp, true, err
}

func (bkr *Broker) initCluster(instanceID structs.ClusterID, port int, details brokerapi.ProvisionDetails) structs.ClusterState {
	return structs.ClusterState{
		InstanceID:       instanceID,
		OrganizationGUID: details.OrganizationGUID,
		PlanID:           details.PlanID,
		ServiceID:        details.ServiceID,
		SpaceGUID:        details.SpaceGUID,
		AllocatedPort:    port,
		AdminCredentials: structs.PostgresCredentials{
			Username: "pgadmin",
			Password: NewPassword(16),
		},
		SuperuserCredentials: structs.PostgresCredentials{
			Username: "postgres",
			Password: NewPassword(16),
		},
		AppCredentials: structs.PostgresCredentials{
			Username: "appuser",
			Password: NewPassword(16),
		},
	}
}

func (bkr *Broker) assertProvisionPrecondition(instanceID structs.ClusterID, features structs.ClusterFeatures) error {
	if bkr.state.ClusterExists(instanceID) {
		return fmt.Errorf("service instance %s already exists", instanceID)
	}

	return bkr.scheduler.VerifyClusterFeatures(features)
}
