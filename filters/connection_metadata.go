package filters

import (
	"net"
	"net/http"
)

// ConnectionMetadata holds data about a connection. No data is ever guaranteed to be present - the
// getter methods, such as ConnectionMetadata.Upstream, may return zero values for missing data.
// TODO: maybe this should be in the proxy package or its own package
type ConnectionMetadata struct {
	// Values from this connection's first request.
	originalURLScheme string
	originalURLHost   string
	originalHost      string

	upstream, downstream net.Conn
	upstreamAddr         string

	requestNumber int

	// Is this part of a MITM'd connection?
	mitming bool

	// Used by proxy.RequestAware connections.
	// TODO: this is horrible; probably evidence this needs to be in the proxy package
	// TODO: could this just be 'lastRequest'?
	requestAwareRequest *http.Request
}

// NewConnectionMetadata creates a new metadata object. Any of the inputs may be nil.
func NewConnectionMetadata(initialReq *http.Request, upstream, downstream net.Conn) *ConnectionMetadata {
	md := &ConnectionMetadata{
		upstream:      upstream,
		downstream:    downstream,
		requestNumber: 1,
		mitming:       false,
	}
	if initialReq != nil {
		md.originalURLScheme = initialReq.URL.Scheme
		md.originalURLHost = initialReq.URL.Host
		md.originalHost = initialReq.Host
	}
	if upstream != nil {
		md.upstreamAddr = upstream.RemoteAddr().String() // TODO: check
	}
	return md
}

// Upstream returns the upstream connection.
func (cm *ConnectionMetadata) Upstream() net.Conn {
	return cm.upstream
}

// UpstreamAddr returns the address of the upstream connection.
func (cm *ConnectionMetadata) UpstreamAddr() string {
	return cm.upstreamAddr
}

// RequestNumber returns the current request number for this connection.
func (cm *ConnectionMetadata) RequestNumber() int {
	return cm.requestNumber
}

// OriginalURLScheme is the value of URL.Scheme taken from the first request received on this
// connection.
func (cm *ConnectionMetadata) OriginalURLScheme() string {
	return cm.originalURLScheme
}

// OriginalURLHost is the value of URL.Host taken from the first request received on this
// connection.
func (cm *ConnectionMetadata) OriginalURLHost() string {
	return cm.originalURLHost
}

// OriginalURLScheme is the value of Host taken from the first request received on this connection.
func (cm *ConnectionMetadata) OriginalHost() string {
	return cm.originalHost
}

// IsMITMing returns true if this connection is part of a MITM'd connection.
func (cm *ConnectionMetadata) IsMITMing() bool {
	return cm.mitming
}

// RequestAwareRequest is the request used by RequestAware connections.
func (cm *ConnectionMetadata) RequestAwareRequest() *http.Request {
	return cm.requestAwareRequest
}

// Clone this object.
func (cm *ConnectionMetadata) Clone() *ConnectionMetadata {
	return &ConnectionMetadata{
		cm.originalURLScheme, cm.originalURLHost, cm.originalHost,
		cm.upstream, cm.downstream, cm.upstreamAddr,
		cm.requestNumber,
		cm.mitming,
		cm.requestAwareRequest,
	}
}

// IncrementRequestNumber increments the counter tracking the number of requests on this connection.
func (cm *ConnectionMetadata) IncrementRequestNumber() {
	cm.requestNumber++
}

// SetUpstream sets the upstream connection.
func (cm *ConnectionMetadata) SetUpstream(upstream net.Conn) {
	cm.upstream = upstream
	cm.upstreamAddr = upstream.RemoteAddr().String() // TODO: should we do this?
}

// SetUpstreamAddr sets the upstream address.
func (cm *ConnectionMetadata) SetUpstreamAddr(upstreamAddr string) {
	cm.upstreamAddr = upstreamAddr
}

// ClearUpstream clears data about the upstream connection such that Upstream and UpstreamAddr
// return zero values.
func (cm *ConnectionMetadata) ClearUpstream() {
	cm.upstream = nil
	cm.upstreamAddr = ""
}

// SetMITMing is used to mark or unmark this connection as part of a MITM'd connection.
func (cm *ConnectionMetadata) SetMITMing(isMITMing bool) {
	cm.mitming = isMITMing
}

// SetRequestAwareRequest sets the request used by RequestAware connections.
func (cm *ConnectionMetadata) SetRequestAwareRequest(req *http.Request) {
	cm.requestAwareRequest = req
}
