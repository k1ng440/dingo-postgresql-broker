package serviceinstance

import (
	"fmt"
	"math/rand"
	"regexp"
	"sort"
	"time"

	"github.com/dingotiles/patroni-broker/backend"
	"github.com/dingotiles/patroni-broker/config"
	"github.com/dingotiles/patroni-broker/utils"
	"github.com/frodenas/brokerapi"
	"github.com/pivotal-golang/lager"
)

// Cluster describes a real/proposed cluster of nodes
type Cluster struct {
	Config           *config.Config
	EtcdClient       backend.EtcdClient
	Logger           lager.Logger
	InstanceID       string
	OrganizationGUID string
	PlanID           string
	ServiceID        string
	SpaceGUID        string
	Parameters       map[string]interface{}
	NodeCount        int
	NodeSize         int
}

// NewCluster creates a RealCluster
func NewCluster(instanceID string, details brokerapi.ProvisionDetails, etcdClient backend.EtcdClient, config *config.Config, logger lager.Logger) (cluster *Cluster) {
	cluster = &Cluster{
		InstanceID:       instanceID,
		OrganizationGUID: details.OrganizationGUID,
		PlanID:           details.PlanID,
		ServiceID:        details.ServiceID,
		SpaceGUID:        details.SpaceGUID,
		EtcdClient:       etcdClient,
		Config:           config,
	}
	if logger != nil {
		cluster.Logger = logger.Session("cluster", lager.Data{
			"instance-id": instanceID,
			"service-id":  details.ServiceID,
			"plan-id":     details.PlanID,
		})
	}
	return
}

// Exists returns true if cluster already exists
func (cluster *Cluster) Exists() bool {
	key := fmt.Sprintf("/serviceinstances/%s/nodes", cluster.InstanceID)
	_, err := cluster.EtcdClient.Get(key, false, true)
	return err == nil
}

// Load the cluster information from KV store
func (cluster *Cluster) Load() error {
	key := fmt.Sprintf("/serviceinstances/%s/nodes", cluster.InstanceID)
	resp, err := cluster.EtcdClient.Get(key, false, true)
	if err != nil {
		cluster.Logger.Error("load.etcd-get", err)
		return err
	}
	cluster.NodeCount = len(resp.Node.Nodes)
	// TODO load current node size
	cluster.NodeSize = 20
	cluster.Logger.Info("load.state", lager.Data{
		"node-count": cluster.NodeCount,
		"node-size":  cluster.NodeSize,
	})
	return nil
}

// WaitForRoutingPortAllocation blocks until the routing tier has allocated a public port
func (cluster *Cluster) WaitForRoutingPortAllocation() (err error) {
	for index := 0; index < 10; index++ {
		key := fmt.Sprintf("/routing/allocation/%s", cluster.InstanceID)
		resp, err := cluster.EtcdClient.Get(key, false, false)
		if err != nil {
			cluster.Logger.Debug("provision.routing", lager.Data{"polling": "allocated-port"})
		} else {
			cluster.Logger.Info("provision.routing", lager.Data{"allocated-port": resp.Node.Value})
			return nil
		}
		time.Sleep(1 * time.Second)
	}
	cluster.Logger.Error("provision.routing", err)
	return err
}

// RandomReplicaNode should discover which nodes are replicas and return a random one
// FIXME - currently just picking a random node - which might be the master
func (cluster *Cluster) RandomReplicaNode() (nodeUUID string, backend string, err error) {
	key := fmt.Sprintf("/serviceinstances/%s/nodes", cluster.InstanceID)
	resp, err := cluster.EtcdClient.Get(key, false, true)
	if err != nil {
		cluster.Logger.Error("random-replica-node.nodes", err)
		return
	}
	item := rand.Intn(len(resp.Node.Nodes))
	nodeKey := resp.Node.Nodes[item].Key
	r, _ := regexp.Compile("/nodes/(.*)$")
	matches := r.FindStringSubmatch(nodeKey)
	nodeUUID = matches[1]

	key = fmt.Sprintf("/serviceinstances/%s/nodes/%s/backend", cluster.InstanceID, nodeUUID)
	resp, err = cluster.EtcdClient.Get(key, false, false)
	if err != nil {
		cluster.Logger.Error("random-replica-node.backend", err)
		return
	}
	backend = resp.Node.Value

	return
}

// AllBackends is a flat list of all Backend APIs
func (cluster *Cluster) AllBackends() (backends []*config.Backend) {
	return cluster.Config.Backends
}

// AllAZs lists of AZs offered by AllBackends()
func (cluster *Cluster) AllAZs() (list []string) {
	azUsage := map[string]int{}
	for _, backend := range cluster.AllBackends() {
		azUsage[backend.AvailabilityZone]++
	}
	for az := range azUsage {
		list = append(list, az)
	}
	// TEST sorting AZs for benefit of tests
	sort.Strings(list)
	return
}

// if any errors, assume that cluster has no running nodes yet
func (cluster *Cluster) usedBackendGUIDs() (backendGUIDs []string) {
	resp, err := cluster.EtcdClient.Get(fmt.Sprintf("/serviceinstances/%s/nodes", cluster.InstanceID), false, false)
	if err != nil {
		return
	}
	for _, clusterNode := range resp.Node.Nodes {
		nodeKey := clusterNode.Key
		resp, err = cluster.EtcdClient.Get(fmt.Sprintf("%s/backend", nodeKey), false, false)
		if err != nil {
			cluster.Logger.Error("az-used.backend", err)
			return
		}
		backendGUIDs = append(backendGUIDs, resp.Node.Value)
	}
	return
}

// backendAZsByUnusedness sorts the availability zones in order of whether this cluster is using them or not
// An AZ that is not being used at all will be early in the result.
// All known AZs are included in the result
func (cluster *Cluster) sortBackendAZsByUnusedness() (vs *utils.ValSorter) {
	backends := cluster.AllBackends()
	azUsageData := map[string]int{}
	for _, az := range cluster.AllAZs() {
		azUsageData[az] = 0
	}
	for _, backendGUID := range cluster.usedBackendGUIDs() {
		for _, backend := range backends {
			if backend.GUID == backendGUID {
				azUsageData[backend.AvailabilityZone]++
			}
		}
	}
	vs = utils.NewValSorter(azUsageData)
	vs.Sort()
	return
}
