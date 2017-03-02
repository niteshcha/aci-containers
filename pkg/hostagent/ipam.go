// Copyright 2017 Cisco Systems, Inc.
//
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

// IP address management for host agent

package hostagent

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"

	"github.com/Sirupsen/logrus"
	cnitypes "github.com/containernetworking/cni/pkg/types"

	"github.com/noironetworks/aci-containers/pkg/ipam"
	"github.com/noironetworks/aci-containers/pkg/metadata"
)

func combine(ranges []*ipam.IpAlloc) *ipam.IpAlloc {
	result := ipam.New()
	for _, r := range ranges {
		result.AddAll(r)
	}
	return result
}

// must have index lock
func (agent *HostAgent) rebuildIpam() {
	for _, md := range agent.epMetadata {
		if md.NetConf.IP4 != nil {
			for _, ipa := range agent.podIpsV4 {
				ipa.RemoveIp(md.NetConf.IP4.IP.IP)
			}
		}
		if md.NetConf.IP6 != nil {
			for _, ipa := range agent.podIpsV6 {
				ipa.RemoveIp(md.NetConf.IP6.IP.IP)
			}
		}
	}

	agent.log.WithFields(logrus.Fields{
		"V4": combine(agent.podIpsV4).FreeList,
		"V6": combine(agent.podIpsV6).FreeList,
	}).Debug("Updated pod network ranges")
}

func (agent *HostAgent) updateIpamAnnotation(newPodNetAnnotation string) {
	if agent.podNetAnnotation == newPodNetAnnotation {
		return
	}
	agent.podNetAnnotation = newPodNetAnnotation

	newRanges := &metadata.NetIps{}
	err := json.Unmarshal([]byte(agent.podNetAnnotation), newRanges)
	if err != nil {
		agent.log.Error("Could not parse pod network annotation", err)
		return
	}

	agent.podIpsV4 = []*ipam.IpAlloc{ipam.New(), ipam.New()}
	if newRanges.V4 != nil {
		agent.podIpsV4[0].AddRanges(newRanges.V4)
	}
	agent.podIpsV6 = []*ipam.IpAlloc{ipam.New(), ipam.New()}
	if newRanges.V6 != nil {
		agent.podIpsV6[0].AddRanges(newRanges.V6)
	}

	agent.rebuildIpam()
}

func convertRoutes(routes []route) []cnitypes.Route {
	cniroutes := make([]cnitypes.Route, 0, len(routes))
	for _, r := range routes {
		cniroutes = append(cniroutes, cnitypes.Route{
			Dst: net.IPNet{
				IP:   r.Dst.IP,
				Mask: r.Dst.Mask,
			},
			GW: r.GW,
		})
	}
	return cniroutes
}

func makeNetconf(nc *cniNetConfig, ip net.IP) *cnitypes.IPConfig {
	return &cnitypes.IPConfig{
		IP: net.IPNet{
			IP:   ip,
			Mask: nc.Subnet.Mask,
		},
		Gateway: nc.Gateway,
		Routes:  convertRoutes(nc.Routes),
	}
}

func allocateIp(free []*ipam.IpAlloc) (net.IP, []*ipam.IpAlloc, error) {
	if len(free) == 0 {
		return nil, free, errors.New("No IP addresses are available")
	}
	ip, err := free[0].GetIp()
	if err != nil {
		return nil, free, err
	}
	if free[0].Empty() {
		return ip, append(free[1:], ipam.New()), nil
	}
	return ip, free, nil
}

func deallocateIp(ip net.IP, free []*ipam.IpAlloc) {
	free[len(free)-1].AddIp(ip)
}

func (agent *HostAgent) allocateIps(netConf *cnitypes.Result) error {
	var v4 net.IP
	var v6 net.IP
	var result error
	var err error

	for _, nc := range agent.config.NetConfig {
		if nc.Subnet.IP != nil {
			if v4 == nil && nc.Subnet.IP.To4() != nil {
				v4, agent.podIpsV4, err = allocateIp(agent.podIpsV4)
				if err != nil {
					result = fmt.Errorf("Could not allocate IPv4 address: %v", err)
				} else {
					netConf.IP4 = makeNetconf(&nc, v4)
				}
			} else if v6 == nil && nc.Subnet.IP.To16() != nil {
				v6, agent.podIpsV6, err = allocateIp(agent.podIpsV6)
				v6, err = agent.podIpsV6[0].GetIp()
				if err != nil {
					result = fmt.Errorf("Could not allocate IPv6 address: %v", err)
				} else {
					netConf.IP6 = makeNetconf(&nc, v6)
				}
			}
		}
	}

	if result != nil {
		netConf.IP4 = nil
		netConf.IP6 = nil
		if v4 != nil {
			deallocateIp(v4, agent.podIpsV4)
		}
		if v6 != nil {
			deallocateIp(v6, agent.podIpsV6)
		}
	} else {
		agent.log.WithFields(logrus.Fields{
			"v4": v4,
			"v6": v6,
		}).Debug("Allocated IP addresses")
	}

	return result
}

func (agent *HostAgent) deallocateIps(netConf *cnitypes.Result) {
	if agent.config.NetConfig == nil {
		// using external ipam
		return
	}
	if netConf.IP4 != nil && netConf.IP4.IP.IP != nil {
		deallocateIp(netConf.IP4.IP.IP, agent.podIpsV4)
		agent.log.WithFields(logrus.Fields{
			"ip": netConf.IP4.IP.IP,
		}).Debug("Returned IP to pool")
	}
	if netConf.IP6 != nil && netConf.IP6.IP.IP != nil {
		deallocateIp(netConf.IP6.IP.IP, agent.podIpsV6)
		agent.log.WithFields(logrus.Fields{
			"ip": netConf.IP6.IP.IP,
		}).Debug("Returned IP to pool")
	}

	return
}