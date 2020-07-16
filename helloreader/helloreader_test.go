package helloreader

import (
	"crypto/tls"
	"io"
	"io/ioutil"
	"net"
	"testing"

	"github.com/getlantern/tlsutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClientHappyPath(t *testing.T) {
	l, err := net.Listen("tcp", "")
	require.NoError(t, err)

	go func() {
		conn, err := l.Accept()
		if !assert.NoError(t, err) {
			return
		}
		assert.NoError(t, tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}}).Handshake())
	}()

	tcpConn, err := net.Dial("tcp", l.Addr().String())
	require.NoError(t, err)

	callbackInvoked := make(chan struct{})
	conn := WrapClient(tcpConn, func(hello []byte, err error) {
		close(callbackInvoked)
		require.NoError(t, err)
		// Testing with tlsutil.ValidateClientHello is a bit circular, but we don't have another
		// easy way to validate the content of hello.
		_, err = tlsutil.ValidateClientHello(hello)
		require.NoError(t, err)
	})
	require.NoError(t, tls.Client(conn, &tls.Config{InsecureSkipVerify: true}).Handshake())
	<-callbackInvoked
}

func TestClientSadPath(t *testing.T) {
	l, err := net.Listen("tcp", "")
	require.NoError(t, err)

	go func() {
		_, err := l.Accept()
		if !assert.NoError(t, err) {
			return
		}
	}()

	tcpConn, err := net.Dial("tcp", l.Addr().String())
	require.NoError(t, err)

	callbackInvoked := make(chan struct{})
	conn := WrapClient(tcpConn, func(hello []byte, err error) {
		close(callbackInvoked)
		require.Error(t, err)
		require.Nil(t, hello)
	})
	_, err = conn.Write([]byte("This is not a valid start to a ClientHello"))
	require.NoError(t, err)
	<-callbackInvoked
}

func TestServerHappyPath(t *testing.T) {
	l, err := net.Listen("tcp", "")
	require.NoError(t, err)

	go func() {
		conn, err := tls.Dial("tcp", l.Addr().String(), &tls.Config{InsecureSkipVerify: true})
		if !assert.NoError(t, err) {
			return
		}
		assert.NoError(t, conn.Handshake())
	}()

	tcpConn, err := l.Accept()
	require.NoError(t, err)

	callbackInvoked := make(chan struct{})
	conn := WrapServer(tcpConn, func(hello []byte, err error) {
		close(callbackInvoked)
		require.NoError(t, err)
		// Testing with tlsutil.ValidateClientHello is a bit circular, but we don't have another
		// easy way to validate the content of hello.
		_, err = tlsutil.ValidateClientHello(hello)
		require.NoError(t, err)
	})
	require.NoError(t, tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}}).Handshake())
	<-callbackInvoked
}

func TestServerSadPath(t *testing.T) {
	l, err := net.Listen("tcp", "")
	require.NoError(t, err)

	go func() {
		conn, err := net.Dial("tcp", l.Addr().String())
		if !assert.NoError(t, err) {
			return
		}
		_, err = conn.Write([]byte("This is not a valid start to a ClientHello"))
		assert.NoError(t, err)
		assert.NoError(t, conn.Close())
	}()

	tcpConn, err := l.Accept()
	require.NoError(t, err)

	callbackInvoked := make(chan struct{})
	conn := WrapServer(tcpConn, func(hello []byte, err error) {
		close(callbackInvoked)
		require.Error(t, err)
		require.Nil(t, hello)
	})
	_, err = io.Copy(ioutil.Discard, conn)
	require.NoError(t, err)
	<-callbackInvoked
}

var (
	certPem = []byte(`-----BEGIN CERTIFICATE-----
MIIBhTCCASugAwIBAgIQIRi6zePL6mKjOipn+dNuaTAKBggqhkjOPQQDAjASMRAw
DgYDVQQKEwdBY21lIENvMB4XDTE3MTAyMDE5NDMwNloXDTE4MTAyMDE5NDMwNlow
EjEQMA4GA1UEChMHQWNtZSBDbzBZMBMGByqGSM49AgEGCCqGSM49AwEHA0IABD0d
7VNhbWvZLWPuj/RtHFjvtJBEwOkhbN/BnnE8rnZR8+sbwnc/KhCk3FhnpHZnQz7B
5aETbbIgmuvewdjvSBSjYzBhMA4GA1UdDwEB/wQEAwICpDATBgNVHSUEDDAKBggr
BgEFBQcDATAPBgNVHRMBAf8EBTADAQH/MCkGA1UdEQQiMCCCDmxvY2FsaG9zdDo1
NDUzgg4xMjcuMC4wLjE6NTQ1MzAKBggqhkjOPQQDAgNIADBFAiEA2zpJEPQyz6/l
Wf86aX6PepsntZv2GYlA5UpabfT2EZICICpJ5h/iI+i341gBmLiAFQOyTDT+/wQc
6MF9+Yw1Yy0t
-----END CERTIFICATE-----`)
	keyPem = []byte(`-----BEGIN EC PRIVATE KEY-----
MHcCAQEEIIrYSSNQFaA2Hwf1duRSxKtLYX5CB04fSeQ6tF1aY/PuoAoGCCqGSM49
AwEHoUQDQgAEPR3tU2Fta9ktY+6P9G0cWO+0kETA6SFs38GecTyudlHz6xvCdz8q
EKTcWGekdmdDPsHloRNtsiCa697B2O9IFA==
-----END EC PRIVATE KEY-----`)

	cert tls.Certificate
)

func init() {
	var err error
	cert, err = tls.X509KeyPair(certPem, keyPem)
	if err != nil {
		panic(err)
	}
}
