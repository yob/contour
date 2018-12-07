// Copyright © 2018 Heptio
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package envoy

import (
	"sort"

	"github.com/envoyproxy/go-control-plane/envoy/api/v2/auth"
	"github.com/envoyproxy/go-control-plane/envoy/api/v2/core"
	"github.com/envoyproxy/go-control-plane/envoy/api/v2/listener"
	"github.com/envoyproxy/go-control-plane/pkg/util"
	"github.com/gogo/protobuf/types"
	"github.com/heptio/contour/internal/dag"
)

// TLSInspector returns a new TLS inspector listener filter.
func TLSInspector() listener.ListenerFilter {
	return listener.ListenerFilter{
		Name:   util.TlsInspector,
		Config: new(types.Struct),
	}
}

// HTTPConnectionManager creates a new HTTP Connection Manager filter
// for the supplied route and access log.
func HTTPConnectionManager(routename, accessLogPath string) listener.Filter {
	return listener.Filter{
		Name: util.HTTPConnectionManager,
		Config: &types.Struct{
			Fields: map[string]*types.Value{
				"stat_prefix": sv(routename),
				"rds": st(map[string]*types.Value{
					"route_config_name": sv(routename),
					"config_source": st(map[string]*types.Value{
						"api_config_source": st(map[string]*types.Value{
							"api_type": sv("GRPC"),
							"grpc_services": lv(
								st(map[string]*types.Value{
									"envoy_grpc": st(map[string]*types.Value{
										"cluster_name": sv("contour"),
									}),
								}),
							),
						}),
					}),
				}),
				"http_filters": lv(
					st(map[string]*types.Value{
						"name": sv(util.Gzip),
					}),
					st(map[string]*types.Value{
						"name": sv(util.GRPCWeb),
					}),
					st(map[string]*types.Value{
						"name": sv(util.Router),
					}),
				),
				"http_protocol_options": st(map[string]*types.Value{
					"accept_http_10": {Kind: &types.Value_BoolValue{BoolValue: true}},
				}),
				"access_log":         accesslog(accessLogPath),
				"use_remote_address": {Kind: &types.Value_BoolValue{BoolValue: true}}, // TODO(jbeda) should this ever be false?
			},
		},
	}
}

// TCPProxy creates a new TCPProxy filter.
func TCPProxy(statPrefix string, proxy *dag.TCPProxy, accessLogPath string) listener.Filter {
	switch len(proxy.Services) {
	case 1:
		return listener.Filter{
			Name: util.TCPProxy,
			Config: &types.Struct{
				Fields: map[string]*types.Value{
					"stat_prefix": sv(statPrefix),
					"cluster":     sv(Clustername(proxy.Services[0])),
					"access_log":  accesslog(accessLogPath),
				},
			},
		}
	default:
		// its easier to sort the input of the cluster list rather than the
		// grpc type output. We have to make a copy to avoid mutating the dag.
		services := make([]*dag.TCPService, len(proxy.Services))
		copy(services, proxy.Services)
		sort.Stable(tcpServiceByName(services))
		var l []*types.Value
		for _, service := range services {
			weight := service.Weight
			if weight == 0 {
				weight = 1
			}
			l = append(l, st(map[string]*types.Value{
				"name":   sv(Clustername(service)),
				"weight": nv(float64(weight)),
			}))
		}
		return listener.Filter{
			Name: util.TCPProxy,
			Config: &types.Struct{
				Fields: map[string]*types.Value{
					"stat_prefix": sv(statPrefix),
					"weighted_clusters": st(map[string]*types.Value{
						"clusters": lv(l...),
					}),
					"access_log": accesslog(accessLogPath),
				},
			},
		}
	}
}

type tcpServiceByName []*dag.TCPService

func (t tcpServiceByName) Len() int      { return len(t) }
func (t tcpServiceByName) Swap(i, j int) { t[i], t[j] = t[j], t[i] }
func (t tcpServiceByName) Less(i, j int) bool {
	if t[i].Name == t[j].Name {
		return t[i].Weight < t[j].Weight
	}
	return t[i].Name < t[j].Name
}

// SocketAddress creates a new TCP core.Address.
func SocketAddress(address string, port int) core.Address {
	return core.Address{
		Address: &core.Address_SocketAddress{
			SocketAddress: &core.SocketAddress{
				Protocol: core.TCP,
				Address:  address,
				PortSpecifier: &core.SocketAddress_PortValue{
					PortValue: uint32(port),
				},
			},
		},
	}
}

// DownstreamTLSContext creates a new DownstreamTlsContext.
func DownstreamTLSContext(cert, key []byte, tlsMinProtoVersion auth.TlsParameters_TlsProtocol, alpnProtos ...string) *auth.DownstreamTlsContext {
	ciphers := []string{
	  "[ECDHE-ECDSA-AES128-GCM-SHA256|ECDHE-ECDSA-CHACHA20-POLY1305]",
	  "[ECDHE-RSA-AES128-GCM-SHA256|ECDHE-RSA-CHACHA20-POLY1305]",
	  "ECDHE-ECDSA-AES128-SHA",
	  "ECDHE-RSA-AES128-SHA",
	  //"AES128-GCM-SHA256",
	  //"AES128-SHA",
	  "ECDHE-ECDSA-AES256-GCM-SHA384",
	  "ECDHE-RSA-AES256-GCM-SHA384",
	  "ECDHE-ECDSA-AES256-SHA",
	  "ECDHE-RSA-AES256-SHA",
	  //"AES256-GCM-SHA384",
	  //"AES256-SHA",
	}
	return &auth.DownstreamTlsContext{
		CommonTlsContext: &auth.CommonTlsContext{
			TlsParams: &auth.TlsParameters{
				TlsMinimumProtocolVersion: tlsMinProtoVersion,
				TlsMaximumProtocolVersion: auth.TlsParameters_TLSv1_3,
				CipherSuites: ciphers,
			},
			TlsCertificates: []*auth.TlsCertificate{{
				CertificateChain: &core.DataSource{
					Specifier: &core.DataSource_InlineBytes{
						InlineBytes: cert,
					},
				},
				PrivateKey: &core.DataSource{
					Specifier: &core.DataSource_InlineBytes{
						InlineBytes: key,
					},
				},
			}},
			AlpnProtocols: alpnProtos,
		},
	}
}

func accesslog(path string) *types.Value {
	return lv(
		st(map[string]*types.Value{
			"name": sv(util.FileAccessLog),
			"config": st(map[string]*types.Value{
				"path": sv(path),
			}),
		}),
	)
}

func sv(s string) *types.Value {
	return &types.Value{Kind: &types.Value_StringValue{StringValue: s}}
}

func st(m map[string]*types.Value) *types.Value {
	return &types.Value{Kind: &types.Value_StructValue{StructValue: &types.Struct{Fields: m}}}
}

func lv(v ...*types.Value) *types.Value {
	return &types.Value{Kind: &types.Value_ListValue{ListValue: &types.ListValue{Values: v}}}
}

func nv(n float64) *types.Value {
	return &types.Value{Kind: &types.Value_NumberValue{NumberValue: n}}
}
