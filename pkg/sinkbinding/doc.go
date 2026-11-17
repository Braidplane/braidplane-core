// Package sinkbinding defines the pipeline sink execution marker and reserved
// parameter names injected by the sink runtime.
//
// Input is the only user-facing binding name that saved sink operations should
// reference. The __braidplane_* names are internal runtime metadata used to preserve
// backend-native behavior behind that single public binding and are rejected
// from user-supplied sink config.
package sinkbinding
