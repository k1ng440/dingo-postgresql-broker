package broker

import (
	"github.com/frodenas/brokerapi"
	"github.com/pivotal-golang/lager"
)

// Unbind to remove access to service instance
func (bkr *Broker) Unbind(instanceID string, bindingID string, details brokerapi.UnbindDetails) error {
	logger := bkr.newLoggingSession("unbind", lager.Data{"instance-id": instanceID})
	defer logger.Info("done")
	return nil
}
