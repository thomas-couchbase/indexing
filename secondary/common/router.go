package common

import mc "github.com/couchbase/indexing/secondary/dcp/transport/client"

// Router definition for each instance (aka engine),
// where an instance might refer to an index or similar entities.
type Router interface {
	// Bucket will return the bucket name for which this
	// router instance is applicable.
	Bucket() string

	// Endpoints return full list of endpoints <host:port>
	// that are listening for this instance.
	Endpoints() []string

	// UpsertEndpoints return a list of endpoints <host:port>
	// to which Upsert message will be published.
	//   * `key` == nil, implies missing secondary key
	//   * `partKey` == nil, implies missing partition key
	//   * m.VBucket, m.Seqno, m.Key - carry {vbno, seqno, docid}
	UpsertEndpoints(m *mc.UprEvent, partKey, key, oldKey []byte) []string

	// UpsertDeletionEndpoints return a list of endpoints
	// <host:port> to which UpsertDeletion message will be
	// published.
	//   * `oldPartKey` and `oldKey` will be computed based on m.OldValue
	//   * `oldKey` == nil, implies old document is not available
	//   * `oldPartKey` == nil, implies old document is not available
	//   * m.VBucket, m.Seqno, m.Key - carry {vbno, seqno, docid}
	// TODO: differentiate between, missing old document and missing
	//       secondary-key
	UpsertDeletionEndpoints(m *mc.UprEvent, oldPartKey, key, oldKey []byte) []string

	// DeletionEndpoints return a list of endpoints
	// <host:port> to which Deletion message will be published.
	//   * `oldPartKey` and `oldKey` will be computed based on m.OldValue
	//   * `oldKey` == nil, implies old document is not available
	//   * `oldPartKey` == nil, implies old document is not available
	//   * m.VBucket, m.Seqno, m.Key - carry {vbno, seqno, docid}
	// TODO: differentiate between, missing old document and missing
	//       secondary-key
	DeletionEndpoints(m *mc.UprEvent, oldPartKey, oldKey []byte) []string
}

// RouterEndpointFactory will create a new endpoint instance for
// {topic, remote-address}
type RouterEndpointFactory func(
	topic, endpointType, raddr string) (RouterEndpoint, error)

// RouterEndpoint abstracts downstream for feed.
type RouterEndpoint interface {
	// Ping will check whether endpoint is active, synchronous call.
	Ping() bool

	// SetConfig will live update configuration parameters for endpoint,
	// synchronous call.
	SetConfig(config Config) error

	// Send will post data to endpoint client, asynchronous call.
	Send(data interface{}) error

	// GetStatistics to gather statistics information from endpoint,
	// synchronous call.
	GetStatistics() map[string]interface{}

	// Close will shutdown this endpoint and release its resources,
	// synchronous call.
	Close() error
}
