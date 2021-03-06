// Copyright (c) 2014 Couchbase, Inc.
// Licensed under the Apache License, Version 2.0 (the "License"); you may not use this file
// except in compliance with the License. You may obtain a copy of the License at
//   http://www.apache.org/licenses/LICENSE-2.0
// Unless required by applicable law or agreed to in writing, software distributed under the
// License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND,
// either express or implied. See the License for the specific language governing permissions
// and limitations under the License.

// TODO:
// 1. functions in this package directly access SystemConfig, instead it
//    is suggested to pass config via function argument.

package indexer

import (
	"errors"
	c "github.com/couchbase/indexing/secondary/common"
	projClient "github.com/couchbase/indexing/secondary/projector/client"
	protobuf "github.com/couchbase/indexing/secondary/protobuf/projector"
	"github.com/couchbaselabs/goprotobuf/proto"
	"time"
)

const (
	HTTP_PREFIX             string = "http://"
	MAX_KV_REQUEST_RETRY    int    = 3
	BACKOFF_FACTOR          int    = 2
	MAX_CLUSTER_FETCH_RETRY int    = 600
)

//KVSender provides the mechanism to talk to KV(projector, router etc)
type KVSender interface {
}

type kvSender struct {
	supvCmdch  MsgChannel //supervisor sends commands on this channel
	supvRespch MsgChannel //channel to send any message to supervisor

	cInfoCache *c.ClusterInfoCache
	config     c.Config
}

func NewKVSender(supvCmdch MsgChannel, supvRespch MsgChannel,
	config c.Config) (KVSender, Message) {

	var cinfo *c.ClusterInfoCache
	url, err := c.ClusterAuthUrl(config["clusterAddr"].String())
	if err == nil {
		cinfo, err = c.NewClusterInfoCache(url, DEFAULT_POOL)
	}
	if err != nil {
		panic("Unable to initialize cluster_info - " + err.Error())
	}
	//Init the kvSender struct
	k := &kvSender{
		supvCmdch:  supvCmdch,
		supvRespch: supvRespch,
		cInfoCache: cinfo,
		config:     config,
	}

	k.cInfoCache.SetMaxRetries(MAX_CLUSTER_FETCH_RETRY)
	k.cInfoCache.SetLogPrefix("KVSender: ")
	//start kvsender loop which listens to commands from its supervisor
	go k.run()

	return k, &MsgSuccess{}

}

//run starts the kvsender loop which listens to messages
//from it supervisor(indexer)
func (k *kvSender) run() {

	//main KVSender loop
loop:
	for {
		select {

		case cmd, ok := <-k.supvCmdch:
			if ok {
				if cmd.GetMsgType() == KV_SENDER_SHUTDOWN {
					c.Infof("KVSender::run Shutting Down")
					k.supvCmdch <- &MsgSuccess{}
					break loop
				}
				k.handleSupvervisorCommands(cmd)
			} else {
				//supervisor channel closed. exit
				break loop
			}

		}
	}
}

func (k *kvSender) handleSupvervisorCommands(cmd Message) {

	switch cmd.GetMsgType() {

	case OPEN_STREAM:
		k.handleOpenStream(cmd)

	case ADD_INDEX_LIST_TO_STREAM:
		k.handleAddIndexListToStream(cmd)

	case REMOVE_INDEX_LIST_FROM_STREAM:
		k.handleRemoveIndexListFromStream(cmd)

	case REMOVE_BUCKET_FROM_STREAM:
		k.handleRemoveBucketFromStream(cmd)

	case CLOSE_STREAM:
		k.handleCloseStream(cmd)

	case KV_SENDER_RESTART_VBUCKETS:
		k.handleRestartVbuckets(cmd)

	default:
		c.Errorf("KVSender::handleSupvervisorCommands "+
			"Received Unknown Command %v", cmd)
	}

}

func (k *kvSender) handleOpenStream(cmd Message) {

	c.Infof("KVSender::handleOpenStream %v", cmd)

	streamId := cmd.(*MsgStreamUpdate).GetStreamId()
	indexInstList := cmd.(*MsgStreamUpdate).GetIndexList()
	restartTs := cmd.(*MsgStreamUpdate).GetRestartTs()
	respCh := cmd.(*MsgStreamUpdate).GetResponseChannel()
	stopCh := cmd.(*MsgStreamUpdate).GetStopChannel()

	go k.openMutationStream(streamId, indexInstList, restartTs, respCh, stopCh)

	k.supvCmdch <- &MsgSuccess{}

}

func (k *kvSender) handleAddIndexListToStream(cmd Message) {

	c.Debugf("KVSender::handleAddIndexListToStream %v", cmd)

	streamId := cmd.(*MsgStreamUpdate).GetStreamId()
	addIndexList := cmd.(*MsgStreamUpdate).GetIndexList()
	respCh := cmd.(*MsgStreamUpdate).GetResponseChannel()
	stopCh := cmd.(*MsgStreamUpdate).GetStopChannel()

	go k.addIndexForExistingBucket(streamId, addIndexList, respCh, stopCh)

	k.supvCmdch <- &MsgSuccess{}
}

func (k *kvSender) handleRemoveIndexListFromStream(cmd Message) {

	c.Debugf("KVSender::handleRemoveIndexListFromStream %v", cmd)

	streamId := cmd.(*MsgStreamUpdate).GetStreamId()
	delIndexList := cmd.(*MsgStreamUpdate).GetIndexList()
	respCh := cmd.(*MsgStreamUpdate).GetResponseChannel()
	stopCh := cmd.(*MsgStreamUpdate).GetStopChannel()

	go k.deleteIndexesFromStream(streamId, delIndexList, respCh, stopCh)

	k.supvCmdch <- &MsgSuccess{}
}

func (k *kvSender) handleRemoveBucketFromStream(cmd Message) {

	c.Debugf("KVSender::handleRemoveBucketFromStream %v", cmd)

	streamId := cmd.(*MsgStreamUpdate).GetStreamId()
	bucket := cmd.(*MsgStreamUpdate).GetBucket()
	respCh := cmd.(*MsgStreamUpdate).GetResponseChannel()
	stopCh := cmd.(*MsgStreamUpdate).GetStopChannel()

	go k.deleteBucketsFromStream(streamId, []string{bucket}, respCh, stopCh)

	k.supvCmdch <- &MsgSuccess{}
}

func (k *kvSender) handleCloseStream(cmd Message) {

	c.Infof("KVSender::handleCloseStream %v", cmd)

	streamId := cmd.(*MsgStreamUpdate).GetStreamId()
	respCh := cmd.(*MsgStreamUpdate).GetResponseChannel()
	stopCh := cmd.(*MsgStreamUpdate).GetStopChannel()

	go k.closeMutationStream(streamId, respCh, stopCh)

	k.supvCmdch <- &MsgSuccess{}
}

func (k *kvSender) handleRestartVbuckets(cmd Message) {

	c.Infof("KVSender::handleRestartVbuckets %v", cmd)

	streamId := cmd.(*MsgRestartVbuckets).GetStreamId()
	restartTs := cmd.(*MsgRestartVbuckets).GetRestartTs()
	respCh := cmd.(*MsgRestartVbuckets).GetResponseCh()
	stopCh := cmd.(*MsgRestartVbuckets).GetStopChannel()

	go k.restartVbuckets(streamId, restartTs, respCh, stopCh)
	k.supvCmdch <- &MsgSuccess{}
}

func (k *kvSender) openMutationStream(streamId c.StreamId, indexInstList []c.IndexInst,
	restartTs *c.TsVbuuid, respCh MsgChannel, stopCh StopChannel) {

	if len(indexInstList) == 0 {
		c.Warnf("KVSender::openMutationStream Empty IndexList. Nothing to do.")
		respCh <- &MsgSuccess{}
		return
	}

	protoInstList := convertIndexListToProto(k.config, k.cInfoCache, indexInstList, streamId)
	bucket := indexInstList[0].Defn.Bucket

	//use any bucket as list of vbs remain the same for all buckets
	vbnos, err := k.getAllVbucketsInCluster(bucket)
	if err != nil {
		c.Errorf("KVSender::openMutationStream \n\t Error in fetching vbuckets info", err)
		respCh <- &MsgError{
			err: Error{code: ERROR_KVSENDER_STREAM_REQUEST_ERROR,
				severity: FATAL,
				cause:    err}}
		return
	}

	restartTsList, err := k.makeRestartTsForVbs(bucket, restartTs, vbnos)
	if err != nil {
		c.Errorf("KVSender::openMutationStream \n\t Error making restart ts", err)
		respCh <- &MsgError{
			err: Error{code: ERROR_KVSENDER_STREAM_REQUEST_ERROR,
				severity: FATAL,
				cause:    err}}
		return
	}

	addrs, err := k.getAllProjectorAddrs()
	if err != nil {
		c.Errorf("KVSender::openMutationStream \n\t Error Fetching Projector Addrs", err)
		respCh <- &MsgError{
			err: Error{code: ERROR_KVSENDER_STREAM_REQUEST_ERROR,
				severity: FATAL,
				cause:    err}}
		return
	}

	var rollbackTs *protobuf.TsVbuuid
	var activeTs *protobuf.TsVbuuid
	topic := getTopicForStreamId(streamId)

	fn := func(r int, err error) error {

		for _, addr := range addrs {

			execWithStopCh(func() {
				ap := newProjClient(addr)
				if res, ret := sendMutationTopicRequest(ap, topic, restartTsList, protoInstList); ret != nil {
					//for all errors, retry
					c.Errorf("KVSender::openMutationStream \n\t Error Received %v from %v", ret, addr)
					err = ret
				} else {
					activeTs = updateActiveTsFromResponse(bucket, activeTs, res)
					rollbackTs = updateRollbackTsFromResponse(bucket, rollbackTs, res)
				}
			}, stopCh)
		}

		if rollbackTs != nil {
			//no retry required for rollback
			return nil
		} else if err != nil {
			//retry for any error
			return err
		} else {
			//check if we have received activeTs for all vbuckets
			retry := false
			if activeTs == nil || activeTs.Len() != len(vbnos) {
				retry = true
			}

			if retry {
				return errors.New("ErrPartialVbStart")
			} else {
				return nil
			}

		}
	}

	rh := c.NewRetryHelper(MAX_KV_REQUEST_RETRY, time.Second, BACKOFF_FACTOR, fn)
	err = rh.Run()

	if rollbackTs != nil {
		c.Infof("KVSender::openMutationStream \n\t Rollback Received %v", rollbackTs)
		//convert from protobuf to native format
		numVbuckets := k.config["numVbuckets"].Int()
		nativeTs := rollbackTs.ToTsVbuuid(numVbuckets)
		respCh <- &MsgRollback{streamId: streamId,
			bucket:     bucket,
			rollbackTs: nativeTs}
	} else if err != nil {
		c.Errorf("KVSender::openMutationStream \n\t Error Received %v", err)
		respCh <- &MsgError{
			err: Error{code: ERROR_KVSENDER_STREAM_REQUEST_ERROR,
				severity: FATAL,
				cause:    err}}
	} else {
		respCh <- &MsgSuccess{}
	}
}

func (k *kvSender) restartVbuckets(streamId c.StreamId, restartTs *c.TsVbuuid,
	respCh MsgChannel, stopCh StopChannel) {

	addrs, err := k.getProjAddrsForVbuckets(restartTs.Bucket, restartTs.GetVbnos())
	if err != nil {
		c.Errorf("KVSender::restartVbuckets \n\t Error in fetching cluster info %v", err)
		respCh <- &MsgError{
			err: Error{code: ERROR_KVSENDER_STREAM_REQUEST_ERROR,
				severity: FATAL,
				cause:    err}}

		return
	}

	//convert TS to protobuf format
	var protoRestartTs *protobuf.TsVbuuid
	numVbuckets := k.config["numVbuckets"].Int()
	protoTs := protobuf.NewTsVbuuid(DEFAULT_POOL, restartTs.Bucket, numVbuckets)
	protoRestartTs = protoTs.FromTsVbuuid(restartTs)

	var rollbackTs *protobuf.TsVbuuid
	topic := getTopicForStreamId(streamId)
	rollback := false

	fn := func(r int, err error) error {

		for _, addr := range addrs {
			ap := newProjClient(addr)

			if res, ret := sendRestartVbuckets(ap, topic, protoRestartTs); ret != nil {
				//retry for all errors
				c.Errorf("KVSender::restartVbuckets \n\t Error Received %v from %v", ret, addr)
				err = ret
			} else {
				rollbackTs = updateRollbackTsFromResponse(restartTs.Bucket, rollbackTs, res)
			}
		}

		if rollbackTs != nil && checkVbListInTS(protoRestartTs.GetVbnos(), rollbackTs) {
			//if rollback, no need to retry
			rollback = true
			return nil
		} else {
			return err
		}
	}

	rh := c.NewRetryHelper(MAX_KV_REQUEST_RETRY, time.Second, BACKOFF_FACTOR, fn)
	err = rh.Run()

	//if any of the requested vb is in rollback ts, send rollback
	//msg to caller
	if rollback {
		//convert from protobuf to native format
		nativeTs := rollbackTs.ToTsVbuuid(numVbuckets)

		respCh <- &MsgRollback{streamId: streamId,
			rollbackTs: nativeTs}
	} else if err != nil {
		//if there is a topicMissing error, a fresh
		//MutationTopicRequest is required.
		if err.Error() == projClient.ErrorTopicMissing.Error() {
			respCh <- &MsgKVStreamRepair{
				streamId: streamId,
				bucket:   restartTs.Bucket,
			}
		} else {
			respCh <- &MsgError{
				err: Error{code: ERROR_KVSENDER_STREAM_REQUEST_ERROR,
					severity: FATAL,
					cause:    err}}

		}
	} else {
		respCh <- &MsgSuccess{}
	}
}

func (k *kvSender) addIndexForExistingBucket(streamId c.StreamId, indexInstList []c.IndexInst,
	respCh MsgChannel, stopCh StopChannel) {

	addrs, err := k.getAllProjectorAddrs()
	if err != nil {
		c.Errorf("KVSender::addIndexForExistingBucket \n\t Error in fetching cluster info", err)
		respCh <- &MsgError{
			err: Error{code: ERROR_KVSENDER_STREAM_REQUEST_ERROR,
				severity: FATAL,
				cause:    err}}
		return
	}

	protoInstList := convertIndexListToProto(k.config, k.cInfoCache, indexInstList, streamId)
	topic := getTopicForStreamId(streamId)

	fn := func(r int, err error) error {

		for _, addr := range addrs {
			execWithStopCh(func() {
				ap := newProjClient(addr)
				if ret := sendAddInstancesRequest(ap, topic, protoInstList); ret != nil {
					c.Errorf("KVSender::addIndexForExistingBucket \n\t Error Received %v from %v", ret, addr)
					err = ret
				}
			}, stopCh)
		}
		return err
	}

	rh := c.NewRetryHelper(MAX_KV_REQUEST_RETRY, time.Second, BACKOFF_FACTOR, fn)
	err = rh.Run()
	if err != nil {
		c.Errorf("KVSender::addIndexForExistingBucket \n\t Error Received %v", err)
		respCh <- &MsgError{
			err: Error{code: ERROR_KVSENDER_STREAM_REQUEST_ERROR,
				severity: FATAL,
				cause:    err}}
		return
	}

	respCh <- &MsgSuccess{}
}

func (k *kvSender) deleteIndexesFromStream(streamId c.StreamId, indexInstList []c.IndexInst,
	respCh MsgChannel, stopCh StopChannel) {

	addrs, err := k.getAllProjectorAddrs()
	if err != nil {
		c.Errorf("KVSender::deleteIndexesFromStream \n\t Error in fetching cluster info", err)
		respCh <- &MsgError{
			err: Error{code: ERROR_KVSENDER_STREAM_REQUEST_ERROR,
				severity: FATAL,
				cause:    err}}
		return
	}

	var uuids []uint64
	for _, indexInst := range indexInstList {
		uuids = append(uuids, uint64(indexInst.InstId))
	}

	topic := getTopicForStreamId(streamId)

	fn := func(r int, err error) error {

		for _, addr := range addrs {
			execWithStopCh(func() {
				ap := newProjClient(addr)
				if ret := sendDelInstancesRequest(ap, topic, uuids); ret != nil {
					c.Errorf("KVSender::deleteIndexesFromStream \n\t Error Received %v from %v", ret, addr)
					if ret.Error() == projClient.ErrorTopicMissing.Error() {
						c.Infof("KVSender::deleteIndexesFromStream Treating TopicMissing As Success")
					} else {
						err = ret
					}
				}
			}, stopCh)
		}
		return err
	}

	rh := c.NewRetryHelper(MAX_KV_REQUEST_RETRY, time.Second, BACKOFF_FACTOR, fn)
	err = rh.Run()
	if err != nil {
		c.Errorf("KVSender::deleteIndexesFromStream \n\t Error Received %v", err)
		respCh <- &MsgError{
			err: Error{code: ERROR_KVSENDER_STREAM_REQUEST_ERROR,
				severity: FATAL,
				cause:    err}}
		return
	}

	respCh <- &MsgSuccess{}
}

func (k *kvSender) deleteBucketsFromStream(streamId c.StreamId, buckets []string,
	respCh MsgChannel, stopCh StopChannel) {

	addrs, err := k.getAllProjectorAddrs()
	if err != nil {
		c.Errorf("KVSender::deleteBucketsFromStream \n\t Error in fetching cluster info", err)
		respCh <- &MsgError{
			err: Error{code: ERROR_KVSENDER_STREAM_REQUEST_ERROR,
				severity: FATAL,
				cause:    err}}
		return
	}

	topic := getTopicForStreamId(streamId)

	fn := func(r int, err error) error {

		for _, addr := range addrs {
			execWithStopCh(func() {
				ap := newProjClient(addr)
				if ret := sendDelBucketsRequest(ap, topic, buckets); ret != nil {
					c.Errorf("KVSender::deleteBucketsFromStream \n\t Error Received %v from %v", ret, addr)
					if ret.Error() == projClient.ErrorTopicMissing.Error() {
						c.Infof("KVSender::deleteBucketsFromStream Treating TopicMissing As Success")
					} else {
						err = ret
					}
				}
			}, stopCh)
		}
		return err
	}

	rh := c.NewRetryHelper(MAX_KV_REQUEST_RETRY, time.Second, BACKOFF_FACTOR, fn)
	err = rh.Run()
	if err != nil {
		c.Errorf("KVSender::deleteBucketsFromStream \n\t Error Received %v", err)
		respCh <- &MsgError{
			err: Error{code: ERROR_KVSENDER_STREAM_REQUEST_ERROR,
				severity: FATAL,
				cause:    err}}
		return
	}

	respCh <- &MsgSuccess{}
}

func (k *kvSender) closeMutationStream(streamId c.StreamId,
	respCh MsgChannel, stopCh StopChannel) {

	addrs, err := k.getAllProjectorAddrs()
	if err != nil {
		c.Errorf("KVSender::closeMutationStream \n\t Error in fetching cluster info", err)
		respCh <- &MsgError{
			err: Error{code: ERROR_KVSENDER_STREAM_REQUEST_ERROR,
				severity: FATAL,
				cause:    err}}
		return
	}

	topic := getTopicForStreamId(streamId)

	fn := func(r int, err error) error {

		for _, addr := range addrs {
			execWithStopCh(func() {
				ap := newProjClient(addr)
				if ret := sendShutdownTopic(ap, topic); ret != nil {
					c.Errorf("KVSender::closeMutationStream \n\t Error Received %v from %v", ret, addr)
					if ret.Error() == projClient.ErrorTopicMissing.Error() {
						c.Infof("KVSender::closeMutationStream Treating TopicMissing As Success")
					} else {
						err = ret
					}
				}
			}, stopCh)
		}
		return err
	}

	rh := c.NewRetryHelper(MAX_KV_REQUEST_RETRY, time.Second, BACKOFF_FACTOR, fn)
	err = rh.Run()
	if err != nil {
		c.Errorf("KVSender::closeMutationStream \n\t Error Received %v", err)
		respCh <- &MsgError{
			err: Error{code: ERROR_KVSENDER_STREAM_REQUEST_ERROR,
				severity: FATAL,
				cause:    err}}
		return
	}

	respCh <- &MsgSuccess{}

}

//send the actual MutationStreamRequest on adminport
func sendMutationTopicRequest(ap *projClient.Client, topic string,
	reqTimestamps *protobuf.TsVbuuid,
	instances []*protobuf.Instance) (*protobuf.TopicResponse, error) {

	c.Debugf("KVSender::sendMutationTopicRequest Projector %v Topic %v Instances %v RequestTS %v",
		ap, topic, instances, reqTimestamps)

	endpointType := "dataport"

	if res, err := ap.MutationTopicRequest(topic, endpointType,
		[]*protobuf.TsVbuuid{reqTimestamps}, instances); err != nil {
		c.Fatalf("KVSender::sendMutationTopicRequest \n\tUnexpected Error %v During Mutation Stream "+
			"Request %v for IndexInst %v", err, topic, instances)

		return res, err
	} else {
		c.Debugf("KVSender::sendMutationTopicRequest \n\tMutationStream Response %v", res)
		return res, nil
	}
}

func sendRestartVbuckets(ap *projClient.Client,
	topic string,
	restartTs *protobuf.TsVbuuid) (*protobuf.TopicResponse, error) {

	c.Debugf("KVSender::sendRestartVbuckets Projector %v Topic %v RestartTs %v",
		ap, topic, restartTs)

	//Shutdown the vbucket before restart. If the vbucket is already
	//running, projector will ignore the request otherwise
	if err := ap.ShutdownVbuckets(topic, []*protobuf.TsVbuuid{restartTs}); err != nil {
		c.Errorf("KVSender::sendRestartVbuckets \n\tUnexpected Error During "+
			"ShutdownVbuckets Request for Topic %v. Err %v.",
			topic, err)

		//all shutdownVbuckets errors are treated as success as it is a best-effort call.
		//RestartVbuckets errors will be acted upon.
	}

	if res, err := ap.RestartVbuckets(topic, []*protobuf.TsVbuuid{restartTs}); err != nil {
		c.Fatalf("KVSender::sendRestartVbuckets \n\tUnexpected Error During "+
			"Restart Vbuckets Request for Topic %v. Err %v.",
			topic, err)

		return res, err
	} else {
		c.Debugf("KVSender::sendRestartVbuckets \n\tRestartVbuckets Response %v", res)
		return res, nil
	}
}

//send the actual AddInstances request on adminport
func sendAddInstancesRequest(ap *projClient.Client,
	topic string,
	instances []*protobuf.Instance) error {

	c.Debugf("KVSender::sendAddInstancesRequest Projector %v Topic %v Instances %v",
		ap, topic, instances)

	if err := ap.AddInstances(topic, instances); err != nil {
		c.Fatalf("KVSender::sendAddInstancesRequest \n\tUnexpected Error During "+
			"Add Instances Request Topic %v IndexInst %v. Err %v",
			topic, instances, err)

		return err
	} else {
		return nil

	}

}

//send the actual DelInstances request on adminport
func sendDelInstancesRequest(ap *projClient.Client,
	topic string,
	uuids []uint64) error {

	c.Debugf("KVSender::sendDelInstancesRequest Projector %v Topic %v Instances %v",
		ap, topic, uuids)

	if err := ap.DelInstances(topic, uuids); err != nil {
		c.Fatalf("KVSender::sendDelInstancesRequest \n\tUnexpected Error During "+
			"Del Instances Request Topic %v Instances %v. Err %v",
			topic, uuids, err)

		return err
	} else {
		return nil

	}

}

//send the actual DelBuckets request on adminport
func sendDelBucketsRequest(ap *projClient.Client,
	topic string,
	buckets []string) error {

	c.Debugf("KVSender::sendDelBucketsRequest Projector %v Topic %v Buckets %v",
		ap, topic, buckets)

	if err := ap.DelBuckets(topic, buckets); err != nil {
		c.Fatalf("KVSender::sendDelBucketsRequest \n\tUnexpected Error During "+
			"Del Buckets Request Topic %v Buckets %v. Err %v",
			topic, buckets, err)

		return err
	} else {
		return nil
	}
}

//send the actual ShutdownStreamRequest on adminport
func sendShutdownTopic(ap *projClient.Client,
	topic string) error {

	c.Debugf("KVSender::sendShutdownTopic Projector %v Topic %v", ap, topic)

	if err := ap.ShutdownTopic(topic); err != nil {
		c.Fatalf("KVSender::sendShutdownTopic \n\tUnexpected Error During "+
			"Shutdown Topic %v. Err %v", topic, err)

		return err
	} else {
		return nil
	}
}

func getTopicForStreamId(streamId c.StreamId) string {

	return StreamTopicName[streamId]

}

func (k *kvSender) makeRestartTsForVbs(bucket string, tsVbuuid *c.TsVbuuid,
	vbnos []uint32) (*protobuf.TsVbuuid, error) {

	var err error

	var ts *protobuf.TsVbuuid
	if tsVbuuid == nil {
		ts, err = k.makeInitialTs(bucket, vbnos)
	} else {
		ts, err = makeRestartTsFromTsVbuuid(bucket, tsVbuuid, vbnos)
	}
	if err != nil {
		return nil, err
	}

	return ts, nil
}

func updateActiveTsFromResponse(bucket string,
	activeTs *protobuf.TsVbuuid, res *protobuf.TopicResponse) *protobuf.TsVbuuid {

	activeTsList := res.GetActiveTimestamps()
	for _, ts := range activeTsList {
		if ts != nil && !ts.IsEmpty() && ts.GetBucket() == bucket {
			if activeTs == nil {
				activeTs = ts.Clone()
			} else {
				activeTs = activeTs.Union(ts)
			}
		}
	}
	return activeTs

}

func updateRollbackTsFromResponse(bucket string,
	rollbackTs *protobuf.TsVbuuid, res *protobuf.TopicResponse) *protobuf.TsVbuuid {

	rollbackTsList := res.GetRollbackTimestamps()
	for _, ts := range rollbackTsList {
		if ts != nil && !ts.IsEmpty() && ts.GetBucket() == bucket {
			if rollbackTs == nil {
				rollbackTs = ts.Clone()
			} else {
				rollbackTs = rollbackTs.Union(ts)
			}
		}
	}

	return rollbackTs

}

func (k *kvSender) makeInitialTs(bucket string,
	vbnos []uint32) (*protobuf.TsVbuuid, error) {

	flogs, err := k.getFailoverLogs(bucket, vbnos)
	if err != nil {
		c.Fatalf("KVSender::makeInitialTs \n\tUnexpected Error During Failover "+
			"Log Request for Bucket %v. Err %v", bucket, err)
		return nil, err
	}

	ts := protobuf.NewTsVbuuid(DEFAULT_POOL, bucket, len(vbnos))
	ts = ts.InitialRestartTs(flogs.ToFailoverLog(c.Vbno32to16(vbnos)))

	return ts, nil
}

func (k *kvSender) makeRestartTsFromKV(bucket string,
	vbnos []uint32) (*protobuf.TsVbuuid, error) {

	flogs, err := k.getFailoverLogs(bucket, vbnos)
	if err != nil {
		c.Fatalf("KVSender::makeRestartTS \n\tUnexpected Error During Failover "+
			"Log Request for Bucket %v. Err %v", bucket, err)
		return nil, err
	}

	ts := protobuf.NewTsVbuuid(DEFAULT_POOL, bucket, len(vbnos))
	ts = ts.ComputeRestartTs(flogs.ToFailoverLog(c.Vbno32to16(vbnos)))

	return ts, nil
}

func makeRestartTsFromTsVbuuid(bucket string, tsVbuuid *c.TsVbuuid,
	vbnos []uint32) (*protobuf.TsVbuuid, error) {

	ts := protobuf.NewTsVbuuid(DEFAULT_POOL, bucket, len(vbnos))
	for _, vbno := range vbnos {
		ts.Append(uint16(vbno), tsVbuuid.Snapshots[vbno][1],
			tsVbuuid.Vbuuids[vbno], tsVbuuid.Snapshots[vbno][0],
			tsVbuuid.Snapshots[vbno][1])
	}

	return ts, nil

}

func (k *kvSender) getFailoverLogs(bucket string,
	vbnos []uint32) (*protobuf.FailoverLogResponse, error) {

	var err error
	var res *protobuf.FailoverLogResponse

	addrs, err := k.getAllProjectorAddrs()
	if err != nil {
		return nil, err
	}

loop:
	for _, addr := range addrs {
		//create client for node's projectors
		client := newProjClient(addr)
		if res, err = client.GetFailoverLogs(DEFAULT_POOL, bucket, vbnos); err == nil {
			break loop
		}
	}

	c.Debugf("KVSender::getFailoverLogs \n\tFailover Log Response %v Error %v", res, err)

	return res, err
}

func (k *kvSender) getAllVbucketsInCluster(bucket string) ([]uint32, error) {

	k.cInfoCache.Lock()
	defer k.cInfoCache.Unlock()

	err := k.cInfoCache.Fetch()
	if err != nil {
		return nil, err
	}

	//get all kv nodes
	nodes, err := k.cInfoCache.GetNodesByBucket(bucket)
	if err != nil {
		return nil, err
	}

	var vbs []uint32
	for _, nid := range nodes {
		//get the list of vbnos for this kv
		if vbnos, err := k.cInfoCache.GetVBuckets(nid, bucket); err != nil {
			return nil, err
		} else {
			vbs = append(vbs, vbnos...)
		}
	}
	return vbs, nil
}

func (k *kvSender) getAllProjectorAddrs() ([]string, error) {

	k.cInfoCache.Lock()
	defer k.cInfoCache.Unlock()

	err := k.cInfoCache.Fetch()
	if err != nil {
		return nil, err
	}

	nodes := k.cInfoCache.GetNodesByServiceType("projector")

	var addrList []string
	for _, nid := range nodes {
		addr, err := k.cInfoCache.GetServiceAddress(nid, "projector")
		if err != nil {
			return nil, err
		}
		addrList = append(addrList, addr)
	}

	return addrList, nil
}

func (k *kvSender) getProjAddrsForVbuckets(bucket string, vbnos []uint16) ([]string, error) {

	k.cInfoCache.Lock()
	defer k.cInfoCache.Unlock()

	err := k.cInfoCache.Fetch()
	if err != nil {
		return nil, err
	}

	var addrList []string

	nodes := k.cInfoCache.GetNodesByServiceType("projector")

	for _, n := range nodes {
		vbs, err := k.cInfoCache.GetVBuckets(n, bucket)
		if err != nil {
			return nil, err
		}
		found := false
	outerloop:
		for _, vb := range vbs {
			for _, vbc := range vbnos {
				if vb == uint32(vbc) {
					found = true
					break outerloop
				}
			}
		}

		if found {
			addr, err := k.cInfoCache.GetServiceAddress(n, "projector")
			if err != nil {
				return nil, err
			}
			addrList = append(addrList, addr)
		}
	}

	return addrList, nil

}

// convert IndexInst to protobuf format
func convertIndexListToProto(cfg c.Config, cinfo *c.ClusterInfoCache, indexList []c.IndexInst,
	streamId c.StreamId) []*protobuf.Instance {

	protoList := make([]*protobuf.Instance, 0)
	for _, index := range indexList {
		protoInst := convertIndexInstToProtoInst(cfg, cinfo, index, streamId)
		protoList = append(protoList, protoInst)
	}

	return protoList

}

// convert IndexInst to protobuf format
func convertIndexInstToProtoInst(cfg c.Config, cinfo *c.ClusterInfoCache,
	indexInst c.IndexInst, streamId c.StreamId) *protobuf.Instance {

	protoDefn := convertIndexDefnToProtobuf(indexInst.Defn)
	protoInst := convertIndexInstToProtobuf(cfg, indexInst, protoDefn)

	addPartnInfoToProtoInst(cfg, cinfo, indexInst, streamId, protoInst)

	return &protobuf.Instance{IndexInstance: protoInst}
}

func convertIndexDefnToProtobuf(indexDefn c.IndexDefn) *protobuf.IndexDefn {

	using := protobuf.StorageType(
		protobuf.StorageType_value[string(indexDefn.Using)]).Enum()
	exprType := protobuf.ExprType(
		protobuf.ExprType_value[string(indexDefn.ExprType)]).Enum()
	partnScheme := protobuf.PartitionScheme(
		protobuf.PartitionScheme_value[string(indexDefn.PartitionScheme)]).Enum()

	defn := &protobuf.IndexDefn{
		DefnID:          proto.Uint64(uint64(indexDefn.DefnId)),
		Bucket:          proto.String(indexDefn.Bucket),
		IsPrimary:       proto.Bool(indexDefn.IsPrimary),
		Name:            proto.String(indexDefn.Name),
		Using:           using,
		ExprType:        exprType,
		SecExpressions:  indexDefn.SecExprs,
		PartitionScheme: partnScheme,
		PartnExpression: proto.String(indexDefn.PartitionKey),
		WhereExpression: proto.String(indexDefn.WhereExpr),
	}

	return defn

}

func convertIndexInstToProtobuf(cfg c.Config, indexInst c.IndexInst,
	protoDefn *protobuf.IndexDefn) *protobuf.IndexInst {

	state := protobuf.IndexState(int32(indexInst.State)).Enum()
	instance := &protobuf.IndexInst{
		InstId:     proto.Uint64(uint64(indexInst.InstId)),
		State:      state,
		Definition: protoDefn,
	}
	return instance
}

func addPartnInfoToProtoInst(cfg c.Config, cinfo *c.ClusterInfoCache,
	indexInst c.IndexInst, streamId c.StreamId, protoInst *protobuf.IndexInst) {

	switch partn := indexInst.Pc.(type) {
	case *c.KeyPartitionContainer:

		//Right now the fill the SinglePartition as that is the only
		//partition structure supported
		partnDefn := partn.GetAllPartitions()

		//TODO move this to indexer init. These addresses cannot change.
		//Better to get these once and store.
		cinfo.Lock()
		defer cinfo.Unlock()

		err := cinfo.Fetch()
		c.CrashOnError(err)

		nid := cinfo.GetCurrentNode()
		streamMaintAddr, err := cinfo.GetServiceAddress(nid, "indexStreamMaint")
		c.CrashOnError(err)
		streamInitAddr, err := cinfo.GetServiceAddress(nid, "indexStreamInit")
		c.CrashOnError(err)
		streamCatchupAddr, err := cinfo.GetServiceAddress(nid, "indexStreamCatchup")
		c.CrashOnError(err)

		var endpoints []string
		for _, p := range partnDefn {
			for _, e := range p.Endpoints() {
				//Set the right endpoint based on streamId
				switch streamId {
				case c.MAINT_STREAM:
					e = c.Endpoint(streamMaintAddr)
				case c.CATCHUP_STREAM:
					e = c.Endpoint(streamCatchupAddr)
				case c.INIT_STREAM:
					e = c.Endpoint(streamInitAddr)
				}
				endpoints = append(endpoints, string(e))
			}
		}
		protoInst.SinglePartn = &protobuf.SinglePartition{
			Endpoints: endpoints,
		}
	}
}

//create client for node's projectors
func newProjClient(addr string) *projClient.Client {

	config := c.SystemConfig.SectionConfig("projector.client.", true)
	config.SetValue("retryInterval", 0) //no retry
	maxvbs := c.SystemConfig["maxVbuckets"].Int()
	return projClient.NewClient(addr, maxvbs, config)

}

func compareIfActiveTsEqual(origTs, compTs *c.TsVbuuid) bool {

	vbnosOrig := origTs.GetVbnos()

	vbnosComp := compTs.GetVbnos()

	for i, vb := range vbnosOrig {
		if vbnosComp[i] != vb {
			return false
		}
	}
	return true

}

//check if any vb in vbList is part of the given ts
func checkVbListInTS(vbList []uint32, ts *protobuf.TsVbuuid) bool {

	for _, vb := range vbList {
		if ts.Contains(uint16(vb)) == true {
			return true
		}
	}
	return false

}

func execWithStopCh(fn func(), stopCh StopChannel) {

	select {

	case <-stopCh:
		stopCh <- true
		return

	default:
		fn()

	}

}

/*--------- Code Archive ----------*/
/*


//RepairEndpoints can only be used if there is a catchup mechanism
//in place as mutations can get missed during repairEndpoint
func (k *kvSender) handleRepairEndpoints(cmd Message) {

	c.Infof("KVSender::handleRepairEndpoints %v", cmd)

	streamId := cmd.(*MsgRepairEndpoints).GetStreamId()
	endpoints := cmd.(*MsgRepairEndpoints).GetEndpoints()

	resp := k.repairEndpoints(streamId, endpoints)
	k.supvCmdch <- resp
}

func (k *kvSender) repairEndpoints(streamId c.StreamId, endpoints []string) Message {
	err := k.cInfoCache.Fetch()
	if err != nil {
		c.Errorf("KVSender::repairEndpoints \n\t Error in fetching cluster info", err)
		return &MsgError{
			err: Error{code: ERROR_KVSENDER_STREAM_REQUEST_ERROR,
				severity: FATAL,
				cause:    err}}
	}

	nodes := k.cInfoCache.GetNodesByServiceType("projector")
	for _, nid := range nodes {
		addr, _ := k.cInfoCache.GetServiceAddress(nid, "projector")
		//create client for node's projectors
		config := c.SystemConfig.SectionConfig("projector.client.", true)
		maxvbs := c.SystemConfig["maxVbuckets"].Int()
		ap := projClient.NewClient(addr, maxvbs, config)

		topic := getTopicForStreamId(streamId)

		if errMsg := sendRepairEndpoints(ap, topic, endpoints); errMsg.GetMsgType() != MSG_SUCCESS {
			return errMsg
		}

	}

	return &MsgSuccess{}
}

func sendRepairEndpoints(ap *projClient.Client,
	topic string,
	endpoints []string) Message {

	c.Debugf("KVSender::sendRepairEndpoints Projector %v Topic %v Endpoints %v",
		ap, topic, endpoints)

	if err := ap.RepairEndpoints(topic, endpoints); err != nil {
		c.Errorf("KVSender::sendRepairEndpoints \n\tUnexpected Error During "+
			"Repair Endpoints Request Topic %v Endpoints %v. Err %v",
			topic, endpoints, err)

		return &MsgError{
			err: Error{code: ERROR_KVSENDER_STREAM_REQUEST_ERROR,
				severity: FATAL,
				cause:    err}}
	} else {
		return &MsgSuccess{}
	}
}

func (k *kvSender) addIndexForNewBucket(streamId c.StreamId, indexInst c.IndexInst) Message {
	err := k.cInfoCache.Fetch()
	if err != nil {
		c.Errorf("KVSender::addIndexForNewBucket \n\t Error in fetching cluster info", err)
		return &MsgError{
			err: Error{code: ERROR_KVSENDER_STREAM_REQUEST_ERROR,
				severity: FATAL,
				cause:    err}}
	}

	protoInst := convertIndexInstToProtoInst(k.config, k.cInfoCache, indexInst, streamId)
	bucket := indexInst.Defn.Bucket
	nodes, _ := k.cInfoCache.GetNodesByBucket(bucket)

	for _, nid := range nodes {
		addr, _ := k.cInfoCache.GetServiceAddress(nid, "projector")
		//create client for node's projectors
		ap := newProjClient(addr)

		//get the list of vbnos for this kv
		vbnos, _ := k.cInfoCache.GetVBuckets(nid, bucket)

		ts, err := k.makeInitialTs(indexInst.Defn.Bucket, vbnos)
		if err != nil {
			return &MsgError{
				err: Error{code: ERROR_KVSENDER_STREAM_REQUEST_ERROR,
					severity: FATAL,
					cause:    err}}
		}
		topic := getTopicForStreamId(streamId)
		restartTs := []*protobuf.TsVbuuid{ts}
		instances := []*protobuf.Instance{protoInst}

		if _, errMsg := sendAddBucketsRequest(ap, topic, restartTs, instances); errMsg.GetMsgType() != MSG_SUCCESS {
			//TODO send message to all KVs to revert the previous requests sent
			return errMsg
		}
	}

	return &MsgSuccess{}
}

//send the actual UpdateMutationStreamRequest on adminport
func sendAddBucketsRequest(ap *projClient.Client,
	topic string,
	restartTs []*protobuf.TsVbuuid,
	instances []*protobuf.Instance) (*protobuf.TopicResponse, Message) {

	c.Debugf("KVSender::sendAddBucketsRequest Projector %v Topic %v Instances %v",
		ap, topic, instances)

	if res, err := ap.AddBuckets(topic, restartTs, instances); err != nil {
		c.Errorf("KVSender::sendAddBucketsRequest \n\tUnexpected Error During "+
			"Mutation Stream Request %v for IndexInst %v. Err %v.",
			topic, instances, err)

		return res, &MsgError{
			err: Error{code: ERROR_KVSENDER_STREAM_REQUEST_ERROR,
				severity: FATAL,
				cause:    err}}
	} else {
		c.Debugf("KVSender::sendAddBucketsRequest \n\tMutationStreamResponse %v", res)
		return res, &MsgSuccess{}
	}
}

func (k *kvSender) handleGetCurrKVTimestamp(cmd Message) {

	//TODO For now Indexer is getting the TS directly from
	//KV. Once Projector API is ready, use that.

}

*/
