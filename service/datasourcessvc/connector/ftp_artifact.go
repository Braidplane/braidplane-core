package connector

import "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/ftp"

// FTPArtifactConnector obtains the private artifact capability from the
// configured factory, including when ordinary execution tracing wraps it.
func FTPArtifactConnector(conn Connector) (*ftp.Connector, bool) {
	if traced, ok := conn.(tracingFileConnector); ok {
		conn = traced.base
	}
	ftpConn, ok := conn.(*ftp.Connector)
	return ftpConn, ok
}
