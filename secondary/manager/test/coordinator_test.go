// Copyright (c) 2014 Couchbase, Inc.
// Licensed under the Apache License, Version 2.0 (the "License"); you may not use this file
// except in compliance with the License. You may obtain a copy of the License at
//   http://www.apache.org/licenses/LICENSE-2.0
// Unless required by applicable law or agreed to in writing, software distributed under the
// License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND,
// either express or implied. See the License for the specific language governing permissions
// and limitations under the License.

package test

import (
	"github.com/couchbase/indexing/secondary/common"
	"github.com/couchbase/indexing/secondary/manager"
	"os"
	"testing"
	"time"
)

// For this test, use index definition id from 200 - 210

func TestCoordinator(t *testing.T) {

	common.LogEnable()
	common.SetLogLevel(common.LogLevelTrace)

	common.Infof("Start TestCoordinator *********************************************************")

	cfg := common.SystemConfig.SectionConfig("indexer", true /*trim*/)
	cfg.Set("storage_dir", common.ConfigValue{"./data/", "metadata file path", "./"})
	os.MkdirAll("./data/", os.ModePerm)

	/*
		var requestAddr = "localhost:9885"
		var leaderAddr = "localhost:9884"
	*/
	var config = "./config.json"
	manager.USE_MASTER_REPO = true
	defer func() { manager.USE_MASTER_REPO = false }()

	mgr, err := manager.NewIndexManagerInternal("localhost:9886", "localhost:"+manager.COORD_MAINT_STREAM_PORT, nil, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	mgr.StartCoordinator(config)
	time.Sleep(time.Duration(1000) * time.Millisecond)

	cleanup(mgr, t)
	time.Sleep(time.Duration(1000) * time.Millisecond)

	// Add a new index definition : 200
	idxDefn := &common.IndexDefn{
		DefnId:          common.IndexDefnId(200),
		Name:            "coordinator_test",
		Using:           common.ForestDB,
		Bucket:          "Default",
		IsPrimary:       false,
		SecExprs:        []string{"Testing"},
		ExprType:        common.N1QL,
		PartitionScheme: common.HASH,
		PartitionKey:    "Testing"}

	err = mgr.HandleCreateIndexDDL(idxDefn)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Duration(1000) * time.Millisecond)

	idxDefn, err = mgr.GetIndexDefnById(common.IndexDefnId(200))
	if err != nil {
		t.Fatal(err)
	}

	if idxDefn == nil {
		t.Fatal("Cannot find index definition")
	}

	topology, err := mgr.GetTopologyByBucket("Default")
	if err != nil {
		t.Fatal(err)
	}
	content, err := manager.MarshallIndexTopology(topology)
	if err != nil {
		t.Fatal(err)
	}
	common.Infof("Topology after index creation : %s", string(content))

	inst := topology.GetIndexInstByDefn(common.IndexDefnId(200))
	if inst == nil || common.IndexState(inst.State) != common.INDEX_STATE_READY {
		t.Fatal("Index Inst not found for index defn 200 or inst state is not in READY")
	}

	cleanup(mgr, t)
	mgr.CleanupTopology()
	mgr.CleanupStabilityTimestamp()
	time.Sleep(time.Duration(1000) * time.Millisecond)

	common.Infof("Done TestCoordinator. Tearing down *********************************************************")
	mgr.Close()
	time.Sleep(time.Duration(1000) * time.Millisecond)
}

// clean up
func cleanup(mgr *manager.IndexManager, t *testing.T) {

	err := mgr.HandleDeleteIndexDDL(common.IndexDefnId(200))
	if err != nil {
		common.Infof("Error deleting index %s:%s, err=%s", "Default", "coordinator_test", err)
	}
	time.Sleep(time.Duration(1000) * time.Millisecond)
}
