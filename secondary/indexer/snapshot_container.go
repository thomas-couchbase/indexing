// Copyright (c) 2014 Couchbase, Inc.
// Licensed under the Apache License, Version 2.0 (the "License"); you may not use this file
// except in compliance with the License. You may obtain a copy of the License at
//   http://www.apache.org/licenses/LICENSE-2.0
// Unless required by applicable law or agreed to in writing, software distributed under the
// License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND,
// either express or implied. See the License for the specific language governing permissions
// and limitations under the License.

package indexer

import (
	"container/list"
	"github.com/couchbase/indexing/secondary/common"
)

// A helper data stucture for in-memory snapshot info list
type SnapshotInfoContainer interface {
	List() []SnapshotInfo

	Add(SnapshotInfo)
	Len() int

	GetLatest() SnapshotInfo
	GetEqualToTS(*common.TsVbuuid) SnapshotInfo
	GetOlderThanTS(*common.TsVbuuid) SnapshotInfo

	RemoveOldest() error
	RemoveRecentThanTS(*common.TsVbuuid) error
	RemoveAll() error
}

type snapshotInfoContainer struct {
	snapshotList *list.List
}

func NewSnapshotInfoContainer(infos []SnapshotInfo) *snapshotInfoContainer {
	sc := &snapshotInfoContainer{snapshotList: list.New()}

	for _, info := range infos {
		sc.snapshotList.PushBack(info)
	}

	return sc
}

func (sc *snapshotInfoContainer) List() []SnapshotInfo {
	var infos []SnapshotInfo
	for e := sc.snapshotList.Front(); e != nil; e = e.Next() {
		info := e.Value.(SnapshotInfo)
		infos = append(infos, info)

	}
	return infos
}

//Add adds snapshot to container
func (sc *snapshotInfoContainer) Add(s SnapshotInfo) {
	sc.snapshotList.PushFront(s)
}

//RemoveOldest removes the oldest snapshot from container.
//Return any error that happened.
func (sc *snapshotInfoContainer) RemoveOldest() error {
	e := sc.snapshotList.Back()

	if e != nil {
		sc.snapshotList.Remove(e)
	}

	return nil
}

//RemoveRecentThanTS discards all the snapshots from container
//which are more recent than the given timestamp. The snaphots
//being removed are closed as well.
func (sc *snapshotInfoContainer) RemoveRecentThanTS(tsVbuuid *common.TsVbuuid) error {
	ts := getStabilityTSFromTsVbuuid(tsVbuuid)
	for e := sc.snapshotList.Front(); e != nil; e = e.Next() {
		snapshot := e.Value.(SnapshotInfo)
		snapTsVbuuid := snapshot.Timestamp()
		snapTs := getStabilityTSFromTsVbuuid(snapTsVbuuid)
		if snapTs.GreaterThan(ts) {
			sc.snapshotList.Remove(e)
		}
	}

	return nil

}

//RemoveAll discards all the snapshosts from container.
//All snapshots will be closed before being discarded.
//Return any error that happened.
func (sc *snapshotInfoContainer) RemoveAll() error {
	//clear the snapshot list
	sc.snapshotList.Init()
	return nil
}

//Len returns the number of snapshots currently in container
func (sc *snapshotInfoContainer) Len() int {
	return sc.snapshotList.Len()
}

//GetLatestSnapshot returns the latest snapshot from container or nil
//in case list is empty
func (sc *snapshotInfoContainer) GetLatest() SnapshotInfo {
	e := sc.snapshotList.Front()

	if e == nil {
		return nil
	} else {
		return e.Value.(SnapshotInfo)
	}
}

//GetSnapshotEqualToTS returns the snapshot from container matching the
//given timestamp or nil if its not able to find any match
func (sc *snapshotInfoContainer) GetEqualToTS(tsVbuuid *common.TsVbuuid) SnapshotInfo {
	ts := getStabilityTSFromTsVbuuid(tsVbuuid)
	for e := sc.snapshotList.Front(); e != nil; e = e.Next() {
		snapshot := e.Value.(SnapshotInfo)
		snapTsVbuuid := snapshot.Timestamp()
		snapTs := getStabilityTSFromTsVbuuid(snapTsVbuuid)
		if ts.Equals(snapTs) {
			return snapshot
		}
	}

	return nil
}

//GetSnapshotOlderThanTS returns a snapshot which is older than the
//given TS or atleast equal. Returns nil if its not able to find any match
func (sc *snapshotInfoContainer) GetOlderThanTS(tsVbuuid *common.TsVbuuid) SnapshotInfo {
	ts := getStabilityTSFromTsVbuuid(tsVbuuid)
	for e := sc.snapshotList.Front(); e != nil; e = e.Next() {
		snapshot := e.Value.(SnapshotInfo)
		snapTsVbuuid := snapshot.Timestamp()
		snapTs := getStabilityTSFromTsVbuuid(snapTsVbuuid)
		if ts.GreaterThanEqual(snapTs) {
			return snapshot
		} else {
			break
		}
	}

	return nil
}
