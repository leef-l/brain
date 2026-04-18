package main

import (
	"github.com/leef-l/brain/cmd/brain/bridge"
	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/tool"
)

// bridge/ package type aliases
type batchPlannerAdapter = bridge.BatchPlannerAdapter
type bridgeTool = bridge.BridgeTool
type specialistToolDef = bridge.SpecialistToolDef
type delegateTool = bridge.DelegateTool

var (
	quantToolDefs              = bridge.QuantToolDefs
	dataToolDefs               = bridge.DataToolDefs
	registerSpecialistBridgeTools = bridge.RegisterSpecialistBridgeTools
	registerDelegateToolForEnvironment = bridge.RegisterDelegateToolForEnvironment
)

func newBatchPlannerAdapter(leaseManager kernel.LeaseManager) *batchPlannerAdapter {
	return bridge.NewBatchPlannerAdapter(leaseManager)
}

func newDelegateTool(orch *kernel.Orchestrator, e *executionEnvironment) *delegateTool {
	return bridge.NewDelegateTool(orch, e)
}

func registerDelegateToolIfAvailable(reg tool.Registry, orch *kernel.Orchestrator, e *executionEnvironment) {
	bridge.RegisterDelegateToolIfAvailable(reg, orch, e)
}
