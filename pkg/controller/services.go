// Copyright 2016 Cisco Systems, Inc.
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

package controller

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"reflect"
	"sort"
	"strconv"

	"github.com/Sirupsen/logrus"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	v1 "k8s.io/client-go/pkg/api/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/kubernetes/pkg/controller"

	"github.com/noironetworks/aci-containers/pkg/apicapi"
	"github.com/noironetworks/aci-containers/pkg/metadata"
)

func (cont *AciController) initEndpointsInformerFromClient(
	kubeClient kubernetes.Interface) {

	cont.initEndpointsInformerBase(&cache.ListWatch{
		ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
			return kubeClient.CoreV1().Endpoints(metav1.NamespaceAll).List(options)
		},
		WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
			return kubeClient.CoreV1().Endpoints(metav1.NamespaceAll).Watch(options)
		},
	})
}

func (cont *AciController) initEndpointsInformerBase(listWatch *cache.ListWatch) {
	cont.endpointsInformer = cache.NewSharedIndexInformer(
		listWatch,
		&v1.Endpoints{},
		controller.NoResyncPeriodFunc(),
		cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc},
	)
	cont.endpointsInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			cont.endpointsChanged(obj)
		},
		UpdateFunc: func(_ interface{}, obj interface{}) {
			cont.endpointsChanged(obj)
		},
		DeleteFunc: func(obj interface{}) {
			cont.endpointsChanged(obj)
		},
	})

}

func (cont *AciController) initServiceInformerFromClient(
	kubeClient *kubernetes.Clientset) {

	cont.initServiceInformerBase(
		&cache.ListWatch{
			ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
				return kubeClient.CoreV1().Services(metav1.NamespaceAll).List(options)
			},
			WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
				return kubeClient.CoreV1().Services(metav1.NamespaceAll).Watch(options)
			},
		})
}

func (cont *AciController) initServiceInformerBase(listWatch *cache.ListWatch) {
	cont.serviceInformer = cache.NewSharedIndexInformer(
		listWatch,
		&v1.Service{},
		controller.NoResyncPeriodFunc(),
		cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc},
	)
	cont.serviceInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			cont.serviceChanged(obj)
		},
		UpdateFunc: func(_ interface{}, obj interface{}) {
			cont.serviceChanged(obj)
		},
		DeleteFunc: func(obj interface{}) {
			cont.serviceDeleted(obj)
		},
	})
}

func serviceLogger(log *logrus.Logger, as *v1.Service) *logrus.Entry {
	return log.WithFields(logrus.Fields{
		"namespace": as.ObjectMeta.Namespace,
		"name":      as.ObjectMeta.Name,
		"type":      as.Spec.Type,
	})
}

func (cont *AciController) endpointsChanged(obj interface{}) {
	servicekey, err := cache.MetaNamespaceKeyFunc(obj.(*v1.Endpoints))
	if err != nil {
		cont.log.Error("Could not create service key: ", err)
		return
	}
	cont.queueServiceUpdateByKey(servicekey)
}

func returnServiceEp(pool *netIps, ep *metadata.ServiceEndpoint) {
	if ep.Ipv4 != nil && ep.Ipv4.To4() != nil {
		pool.V4.AddIp(ep.Ipv4)
	}
	if ep.Ipv6 != nil && ep.Ipv6.To16() != nil {
		pool.V6.AddIp(ep.Ipv6)
	}
}

func returnIps(pool *netIps, ips []net.IP) {
	for _, ip := range ips {
		if ip == nil {
			continue
		}
		if ip.To4() != nil {
			pool.V4.AddIp(ip)
		} else if ip.To16() != nil {
			pool.V6.AddIp(ip)
		}
	}
}

func (cont *AciController) staticServiceObjs() apicapi.ApicSlice {
	// Service bridge domain
	bdName := cont.aciNameForKey("bd", "kubernetes-service")

	bd := apicapi.NewFvBD(cont.config.AciVrfTenant, bdName)
	bd.SetAttr("arpFlood", "yes")
	bd.SetAttr("ipLearning", "no")
	bd.SetAttr("unkMacUcastAct", "flood")
	bdToOut := apicapi.NewRsBdToOut(bd.GetDn(), cont.config.AciL3Out)
	bd.AddChild(bdToOut)
	bdToVrf := apicapi.NewRsCtx(bd.GetDn(), cont.config.AciVrf)
	bd.AddChild(bdToVrf)

	bdn := bd.GetDn()
	for _, cidr := range cont.config.NodeServiceSubnets {
		sn := apicapi.NewFvSubnet(bdn, cidr)
		bd.AddChild(sn)
	}

	return apicapi.ApicSlice{bd}
}

func (cont *AciController) initStaticServiceObjs() {
	cont.apicConn.WriteApicObjects(cont.config.AciPrefix+"_service_static",
		cont.staticServiceObjs())
}

// can be called with index lock
func (cont *AciController) updateServicesForNode(nodename string) {
	cache.ListAll(cont.endpointsInformer.GetIndexer(), labels.Everything(),
		func(endpointsobj interface{}) {
			endpoints := endpointsobj.(*v1.Endpoints)
			for _, subset := range endpoints.Subsets {
				for _, addr := range subset.Addresses {
					if addr.NodeName != nil && *addr.NodeName == nodename {

						servicekey, err :=
							cache.MetaNamespaceKeyFunc(endpointsobj.(*v1.Endpoints))
						if err != nil {
							cont.log.Error("Could not create endpoints key: ", err)
							return
						}
						cont.queueServiceUpdateByKey(servicekey)
						return
					}
				}
			}
		})
}

// must have index lock
func (cont *AciController) fabricPathForNode(name string) (string, bool) {
	for _, device := range cont.nodeOpflexDevice[name] {
		return device.GetAttrStr("fabricPathDn"), true
	}
	return "", false
}

func apicRedirectPol(name string, tenantName string, nodes []string,
	nodeMap map[string]*metadata.ServiceEndpoint) (apicapi.ApicObject, string) {
	rp := apicapi.NewVnsSvcRedirectPol(tenantName, name)
	rpDn := rp.GetDn()
	for _, node := range nodes {
		serviceEp, ok := nodeMap[node]
		if !ok {
			continue
		}
		if serviceEp.Ipv4 != nil {
			rp.AddChild(apicapi.NewVnsRedirectDest(rpDn,
				serviceEp.Ipv4.String(), serviceEp.Mac))
		}
		if serviceEp.Ipv6 != nil {
			rp.AddChild(apicapi.NewVnsRedirectDest(rpDn,
				serviceEp.Ipv6.String(), serviceEp.Mac))
		}
	}
	return rp, rpDn
}

func apicExtNet(name string, tenantName string, l3Out string,
	ingresses []string) apicapi.ApicObject {

	en := apicapi.NewL3extInstP(tenantName, l3Out, name)
	enDn := en.GetDn()
	en.AddChild(apicapi.NewFvRsProv(enDn, name))
	for _, ingress := range ingresses {
		en.AddChild(apicapi.NewL3extSubnet(enDn, ingress+"/32"))
	}
	return en
}

func apicExtNetCons(conName string, tenantName string,
	l3Out string, net string) apicapi.ApicObject {

	enDn := fmt.Sprintf("uni/tn-%s/out-%s/instP-%s", tenantName, l3Out, net)
	return apicapi.NewFvRsCons(enDn, conName)
}

func apicContract(conName string, tenantName string,
	graphName string) apicapi.ApicObject {
	con := apicapi.NewVzBrCP(tenantName, conName)
	cs := apicapi.NewVzSubj(con.GetDn(), "loadbalancedservice")
	csDn := cs.GetDn()
	cs.AddChild(apicapi.NewVzRsSubjGraphAtt(csDn, graphName))
	cs.AddChild(apicapi.NewVzRsSubjFiltAtt(csDn, conName))
	con.AddChild(cs)
	return con
}

func apicDevCtx(name string, tenantName string,
	graphName string, bdName string, rpDn string) apicapi.ApicObject {

	cc := apicapi.NewVnsLDevCtx(tenantName, name, graphName, "loadbalancer")
	ccDn := cc.GetDn()
	graphDn := fmt.Sprintf("uni/tn-%s/lDevVip-%s", tenantName, graphName)
	lifDn := fmt.Sprintf("%s/lIf-%s", graphDn, "interface")
	bdDn := fmt.Sprintf("uni/tn-%s/BD-%s", tenantName, bdName)
	cc.AddChild(apicapi.NewVnsRsLDevCtxToLDev(ccDn, graphDn))
	for _, ctxConn := range []string{"consumer", "provider"} {
		lifCtx := apicapi.NewVnsLIfCtx(ccDn, ctxConn)
		lifCtxDn := lifCtx.GetDn()
		lifCtx.AddChild(apicapi.NewVnsRsLIfCtxToSvcRedirectPol(lifCtxDn,
			rpDn))
		lifCtx.AddChild(apicapi.NewVnsRsLIfCtxToBD(lifCtxDn, bdDn))
		lifCtx.AddChild(apicapi.NewVnsRsLIfCtxToLIf(lifCtxDn, lifDn))
		cc.AddChild(lifCtx)
	}

	return cc
}

func (cont *AciController) updateServiceDeviceInstance(key string,
	service *v1.Service) {

	nodeMap, nodes := cont.getNodesForService(key, service)

	name := cont.aciNameForKey("svc", key)
	graphName := cont.aciNameForKey("svc", "global")
	var serviceObjs apicapi.ApicSlice

	if len(nodes) > 0 {

		// 1. Service redirect policy
		// The service redirect policy contains the MAC address
		// and IP address of each of the service endpoints for
		// each node that hosts a pod for this service.  The
		// example below shows the case of two nodes.
		rp, rpDn :=
			apicRedirectPol(name, cont.config.AciVrfTenant, nodes, nodeMap)
		serviceObjs = append(serviceObjs, rp)

		// 2. Service graph contract and external network
		// The service graph contract must be bound to the service
		// graph.  This contract must be consumed by the default
		// layer 3 network and provided by the service layer 3
		// network.
		{
			var ingresses []string
			for _, ingress := range service.Status.LoadBalancer.Ingress {
				ingresses = append(ingresses, ingress.IP)
			}
			serviceObjs = append(serviceObjs,
				apicExtNet(name, cont.config.AciVrfTenant,
					cont.config.AciL3Out, ingresses))
		}

		serviceObjs = append(serviceObjs,
			apicContract(name, cont.config.AciVrfTenant, graphName))

		for _, net := range cont.config.AciExtNetworks {
			serviceObjs = append(serviceObjs,
				apicExtNetCons(name, cont.config.AciVrfTenant,
					cont.config.AciL3Out, net))
		}
		{
			filter := apicapi.NewVzFilter(cont.config.AciVrfTenant, name)
			filterDn := filter.GetDn()

			for i, port := range service.Spec.Ports {
				fe := apicapi.NewVzEntry(filterDn, strconv.Itoa(i))
				fe.SetAttr("etherT", "ip")
				if port.Protocol == v1.ProtocolUDP {
					fe.SetAttr("prot", "udp")
				} else {
					fe.SetAttr("prot", "tcp")
				}
				pstr := strconv.Itoa(int(port.Port))
				fe.SetAttr("dFromPort", pstr)
				fe.SetAttr("dToPort", pstr)
				filter.AddChild(fe)
			}
			serviceObjs = append(serviceObjs, filter)
		}

		// 3. Device cluster context
		// The logical device context binds the service contract
		// to the redirect policy and the device cluster and
		// bridge domain for the device cluster.
		serviceObjs = append(serviceObjs,
			apicDevCtx(name, cont.config.AciVrfTenant, graphName,
				cont.aciNameForKey("bd", "kubernetes-service"), rpDn))
	}

	cont.apicConn.WriteApicObjects(name, serviceObjs)
}

func (cont *AciController) queueServiceUpdateByKey(key string) {
	cont.serviceQueue.Add(key)
}

func (cont *AciController) queueServiceUpdate(service *v1.Service) {
	key, err := cache.MetaNamespaceKeyFunc(service)
	if err != nil {
		serviceLogger(cont.log, service).
			Error("Could not create service key: ", err)
		return
	}
	cont.serviceQueue.Add(key)
}

func apicDeviceCluster(name string, vrfTenant string,
	physDom string, encap string,
	nodes []string, nodeMap map[string]string) (apicapi.ApicObject, string) {

	dc := apicapi.NewVnsLDevVip(vrfTenant, name)
	dc.SetAttr("managed", "no")
	dcDn := dc.GetDn()
	dc.AddChild(apicapi.NewVnsRsALDevToPhysDomP(dcDn,
		fmt.Sprintf("uni/phys-%s", physDom)))
	lif := apicapi.NewVnsLIf(dcDn, "interface")
	lif.SetAttr("encap", encap)
	lifDn := lif.GetDn()

	for _, node := range nodes {
		path, ok := nodeMap[node]
		if !ok {
			continue
		}

		cdev := apicapi.NewVnsCDev(dcDn, node)
		cif := apicapi.NewVnsCif(cdev.GetDn(), "interface")
		cif.AddChild(apicapi.NewVnsRsCIfPathAtt(cif.GetDn(), path))
		cdev.AddChild(cif)
		lif.AddChild(apicapi.NewVnsRsCIfAttN(lifDn, cif.GetDn()))
		dc.AddChild(cdev)
	}

	dc.AddChild(lif)

	return dc, dcDn
}

func apicServiceGraph(name string, tenantName string,
	dcDn string) apicapi.ApicObject {

	sg := apicapi.NewVnsAbsGraph(tenantName, name)
	sgDn := sg.GetDn()
	var provDn string
	var consDn string
	var cTermDn string
	var pTermDn string
	{
		an := apicapi.NewVnsAbsNode(sgDn, "loadbalancer")
		an.SetAttr("managed", "no")
		an.SetAttr("routingMode", "Redirect")
		anDn := an.GetDn()
		cons := apicapi.NewVnsAbsFuncConn(anDn, "consumer")
		consDn = cons.GetDn()
		an.AddChild(cons)
		prov := apicapi.NewVnsAbsFuncConn(anDn, "provider")
		provDn = prov.GetDn()
		an.AddChild(prov)
		an.AddChild(apicapi.NewVnsRsNodeToLDev(anDn, dcDn))
		sg.AddChild(an)
	}
	{
		tnc := apicapi.NewVnsAbsTermNodeCon(sgDn, "T1")
		tncDn := tnc.GetDn()
		cTerm := apicapi.NewVnsAbsTermConn(tncDn)
		cTermDn = cTerm.GetDn()
		tnc.AddChild(cTerm)
		tnc.AddChild(apicapi.NewVnsInTerm(tncDn))
		tnc.AddChild(apicapi.NewVnsOutTerm(tncDn))
		sg.AddChild(tnc)
	}
	{
		tnp := apicapi.NewVnsAbsTermNodeProv(sgDn, "T2")
		tnpDn := tnp.GetDn()
		pTerm := apicapi.NewVnsAbsTermConn(tnpDn)
		pTermDn = pTerm.GetDn()
		tnp.AddChild(pTerm)
		tnp.AddChild(apicapi.NewVnsInTerm(tnpDn))
		tnp.AddChild(apicapi.NewVnsOutTerm(tnpDn))
		sg.AddChild(tnp)
	}
	{
		acc := apicapi.NewVnsAbsConnection(sgDn, "C1")
		acc.SetAttr("connDir", "provider")
		accDn := acc.GetDn()
		acc.AddChild(apicapi.NewVnsRsAbsConnectionConns(accDn, consDn))
		acc.AddChild(apicapi.NewVnsRsAbsConnectionConns(accDn, cTermDn))
		sg.AddChild(acc)
	}
	{
		acp := apicapi.NewVnsAbsConnection(sgDn, "C2")
		acp.SetAttr("connDir", "provider")
		acpDn := acp.GetDn()
		acp.AddChild(apicapi.NewVnsRsAbsConnectionConns(acpDn, provDn))
		acp.AddChild(apicapi.NewVnsRsAbsConnectionConns(acpDn, pTermDn))
		sg.AddChild(acp)
	}
	return sg
}
func (cont *AciController) updateDeviceCluster() {
	nodeMap := make(map[string]string)

	cont.indexMutex.Lock()
	cache.ListAll(cont.nodeInformer.GetStore(), labels.Everything(),
		func(nodeobj interface{}) {
			name := nodeobj.(*v1.Node).ObjectMeta.Name

			fabricPath, ok := cont.fabricPathForNode(name)
			if !ok {
				return
			}

			nodeMap[name] = fabricPath
		})
	cont.indexMutex.Unlock()

	var nodes []string
	for node, _ := range nodeMap {
		nodes = append(nodes, node)
	}
	sort.Strings(nodes)

	name := cont.aciNameForKey("svc", "global")
	var serviceObjs apicapi.ApicSlice

	// 1. Device cluster:
	// The device cluster is a set of physical paths that need to be
	// created for each node in the cluster, that correspond to the
	// service interface for each node.
	dc, dcDn := apicDeviceCluster(name, cont.config.AciVrfTenant,
		cont.config.AciServicePhysDom, cont.config.AciServiceEncap,
		nodes, nodeMap)
	serviceObjs = append(serviceObjs, dc)

	// 2. Service graph template
	// The service graph controls how the traffic will be redirected.
	// A service graph must be created for each device cluster.
	serviceObjs = append(serviceObjs,
		apicServiceGraph(name, cont.config.AciVrfTenant, dcDn))

	cont.apicConn.WriteApicObjects(name, serviceObjs)
}

func (cont *AciController) fabricPathLogger(node string,
	obj apicapi.ApicObject) *logrus.Entry {

	return cont.log.WithFields(logrus.Fields{
		"fabricPath": obj.GetAttr("fabricPathDn"),
		"node":       node,
	})
}

func (cont *AciController) opflexDeviceChanged(obj apicapi.ApicObject) {
	var nodeUpdates []string

	cont.indexMutex.Lock()
	nodefound := false
	for node, devices := range cont.nodeOpflexDevice {
		found := false

		if node == obj.GetAttrStr("hostName") {
			nodefound = true
		}

		for i, device := range devices {
			if device.GetDn() != obj.GetDn() {
				continue
			}
			found = true

			if obj.GetAttrStr("hostName") != node {
				cont.fabricPathLogger(node, device).
					Debug("Moving opflex device path from node")

				devices = append(devices[:i], devices[i+1:]...)
				cont.nodeOpflexDevice[node] = devices
				nodeUpdates = append(nodeUpdates, node)
				break
			} else if device.GetAttrStr("fabricPathDn") !=
				obj.GetAttrStr("fabricPathDn") {
				cont.fabricPathLogger(node, obj).
					Debug("Updating opflex device path")

				devices = append(append(devices[:i], devices[i+1:]...), obj)
				cont.nodeOpflexDevice[node] = devices
				nodeUpdates = append(nodeUpdates, node)
				break
			}
		}
		if !found && obj.GetAttrStr("hostName") == node {
			cont.fabricPathLogger(node, obj).
				Debug("Appending opflex device path")

			devices = append(devices, obj)
			cont.nodeOpflexDevice[node] = devices
			nodeUpdates = append(nodeUpdates, node)
		}
	}
	if !nodefound {
		node := obj.GetAttrStr("hostName")
		cont.fabricPathLogger(node, obj).Debug("Adding opflex device path")
		cont.nodeOpflexDevice[node] = apicapi.ApicSlice{obj}
		nodeUpdates = append(nodeUpdates, node)
	}
	cont.indexMutex.Unlock()

	cont.updateDeviceCluster()
	for _, node := range nodeUpdates {
		cont.updateServicesForNode(node)
	}
}

func (cont *AciController) opflexDeviceDeleted(dn string) {
	cont.log.Debug("odev Deleted ", dn)

	var nodeUpdates []string

	cont.indexMutex.Lock()
	for node, devices := range cont.nodeOpflexDevice {
		for i, device := range devices {
			if device.GetDn() != dn {
				continue
			}

			cont.fabricPathLogger(node, device).
				Debug("Deleting opflex device path")
			devices = append(devices[:i], devices[i+1:]...)
			cont.nodeOpflexDevice[node] = devices
			nodeUpdates = append(nodeUpdates, node)
			break
		}
		if len(devices) == 0 {
			delete(cont.nodeOpflexDevice, node)
		}
	}
	cont.indexMutex.Unlock()

	cont.updateDeviceCluster()
	for _, node := range nodeUpdates {
		cont.updateServicesForNode(node)
	}
}

func (cont *AciController) serviceChanged(obj interface{}) {
	cont.queueServiceUpdate(obj.(*v1.Service))
}

func (cont *AciController) serviceFullSync() {
	cache.ListAll(cont.serviceInformer.GetIndexer(), labels.Everything(),
		func(sobj interface{}) {
			cont.queueServiceUpdate(sobj.(*v1.Service))
		})
}

func (cont *AciController) writeApicSvc(key string, service *v1.Service) {
	endpointsobj, _, err :=
		cont.endpointsInformer.GetStore().GetByKey(key)
	if err != nil {
		cont.log.Error("Could not lookup endpoints for " +
			key + ": " + err.Error())
		return
	}

	aobj := apicapi.NewVmmInjectedSvc("Kubernetes",
		cont.config.AciVmmDomain, cont.config.AciVmmController,
		service.Namespace, service.Name)
	aobjDn := aobj.GetDn()
	aobj.SetAttr("guid", string(service.UID))
	// APIC model only allows one of these
	for _, ingress := range service.Status.LoadBalancer.Ingress {
		aobj.SetAttr("lbIp", ingress.IP)
		break
	}
	aobj.SetAttr("clusterIp", string(service.Spec.ClusterIP))
	var t string
	switch service.Spec.Type {
	case v1.ServiceTypeClusterIP:
		t = "clusterIp"
	case v1.ServiceTypeNodePort:
		t = "nodePort"
	case v1.ServiceTypeLoadBalancer:
		t = "loadBalancer"
	case v1.ServiceTypeExternalName:
		t = "externalName"
	}
	aobj.SetAttr("type", t)
	for _, port := range service.Spec.Ports {
		var proto string
		if port.Protocol == v1.ProtocolUDP {
			proto = "udp"
		} else {
			proto = "tcp"
		}
		p := apicapi.NewVmmInjectedSvcPort(aobjDn,
			strconv.Itoa(int(port.Port)), proto, port.TargetPort.String())
		p.SetAttr("nodePort", strconv.Itoa(int(port.NodePort)))
		aobj.AddChild(p)
	}
	if endpointsobj != nil {
		for _, subset := range endpointsobj.(*v1.Endpoints).Subsets {
			for _, addr := range subset.Addresses {
				if addr.TargetRef == nil || addr.TargetRef.Kind != "Pod" {
					continue
				}
				aobj.AddChild(apicapi.NewVmmInjectedSvcEp(aobjDn,
					addr.TargetRef.Name))
			}
		}
	}

	name := cont.aciNameForKey("service-vmm", key)
	cont.apicConn.WriteApicObjects(name, apicapi.ApicSlice{aobj})
}

func (cont *AciController) allocateNodeServiceEps(servicekey string,
	service *v1.Service) {

	endpointsobj, exists, err :=
		cont.endpointsInformer.GetStore().GetByKey(servicekey)
	if err != nil {
		cont.log.Error("Could not lookup endpoints for "+
			servicekey, ": ", err)
		return
	}

	logger := serviceLogger(cont.log, service)

	cont.indexMutex.Lock()
	meta, ok := cont.serviceMetaCache[servicekey]
	if !ok {
		cont.indexMutex.Unlock()
		return
	}

	if service.Annotations == nil {
		service.Annotations = make(map[string]string)
	}
	annotMap := make(map[string]*metadata.ServiceEndpoint)
	if epval, epok := service.Annotations[metadata.ServiceEpAnnotation]; epok {
		err := json.Unmarshal([]byte(epval), &annotMap)
		if err != nil {
			logger.WithFields(logrus.Fields{
				"epval": epval,
			}).Error("Could not parse existing node ",
				"service endpoint annotation: ", err)
		}
		for node, ep := range annotMap {
			_, ok := meta.nodeServiceEps[node]
			if ok {
				continue
			}
			valid := true
			_, err := net.ParseMAC(ep.Mac)
			if err != nil {
				logger.Error("Invalid MAC in existing ",
					"node service endpoint: ", ep.Mac, ": ", err)
				continue
			}
			if ep.Ipv4 != nil {
				if !cont.nodeServiceIps.V4.RemoveIp(ep.Ipv4) {
					logger.Error("IPv4 address not available ",
						"for node service endpoint: ", ep.Ipv4)
					valid = false
				}
			}
			if valid && ep.Ipv6 != nil {
				if !cont.nodeServiceIps.V6.RemoveIp(ep.Ipv6) {
					logger.Error("IPv6 address not available ",
						"for node service endpoint: ", ep.Ipv6)
					valid = false
					if ep.Ipv4 != nil {
						cont.nodeServiceIps.V4.AddIp(ep.Ipv4)
					}
				}
			}
			if valid {
				meta.nodeServiceEps[node] = ep
			}
		}
	}

	if !cont.serviceSyncEnabled {
		cont.indexMutex.Unlock()
		return
	}

	nodeMap := make(map[string]bool)
	if exists && endpointsobj != nil {
		endpoints := endpointsobj.(*v1.Endpoints)
		for _, subset := range endpoints.Subsets {
			for _, addr := range subset.Addresses {
				if addr.NodeName == nil {
					continue
				}
				nodeMap[*addr.NodeName] = true
			}
		}
	}
	for node, _ := range nodeMap {
		if _, ok = meta.nodeServiceEps[node]; !ok {
			newep, err := cont.createServiceEndpoint()
			if err != nil {
				cont.log.Error("Could not allocate service endpoint: ", err)
				continue
			}
			meta.nodeServiceEps[node] = newep
		}
	}
	for node, _ := range meta.nodeServiceEps {
		if _, ok = nodeMap[node]; !ok {
			delete(meta.nodeServiceEps, node)
		}
	}

	if !reflect.DeepEqual(meta.nodeServiceEps, annotMap) {
		raw, err := json.Marshal(meta.nodeServiceEps)
		if err != nil {
			logger.Error("Could not create node service endpoint annotation", err)
		} else {
			service.Annotations[metadata.ServiceEpAnnotation] = string(raw)
		}

		_, err = cont.updateService(service)
		if err != nil {
			logger.Error("Failed to update service: ", err)
		} else {
			logger.WithFields(logrus.Fields{
				"annot": service.Annotations[metadata.ServiceEpAnnotation],
			}).Debug("Updated service node ep annotation")
		}
	}

	cont.indexMutex.Unlock()

}

func (cont *AciController) allocateServiceIps(servicekey string,
	service *v1.Service) {
	logger := serviceLogger(cont.log, service)

	cont.indexMutex.Lock()
	meta := cont.serviceMetaCache[servicekey]

	// Read any existing IPs and attempt to allocate them to the pod
	for _, ingress := range service.Status.LoadBalancer.Ingress {
		ip := net.ParseIP(ingress.IP)
		if ip == nil {
			continue
		}
		if ip.To4() != nil {
			if cont.serviceIps.V4.RemoveIp(ip) {
				meta.ingressIps = append(meta.ingressIps, ip)
			} else if cont.staticServiceIps.V4.RemoveIp(ip) {
				meta.staticIngressIps = append(meta.staticIngressIps, ip)
			}
		} else if ip.To16() != nil {
			if cont.serviceIps.V6.RemoveIp(ip) {
				meta.ingressIps = append(meta.ingressIps, ip)
			} else if cont.staticServiceIps.V6.RemoveIp(ip) {
				meta.staticIngressIps = append(meta.staticIngressIps, ip)
			}
		}
	}

	if !cont.serviceSyncEnabled {
		cont.indexMutex.Unlock()
		return
	}

	// try to give the requested load balancer IP to the pod
	requestedIp := net.ParseIP(service.Spec.LoadBalancerIP)
	if requestedIp != nil {
		hasRequestedIp := false
		for _, ip := range meta.ingressIps {
			if reflect.DeepEqual(requestedIp, ip) {
				hasRequestedIp = true
			}
		}
		if !hasRequestedIp {
			if requestedIp.To4() != nil &&
				cont.staticServiceIps.V4.RemoveIp(requestedIp) {
				hasRequestedIp = true
			} else if requestedIp.To16() != nil &&
				cont.staticServiceIps.V6.RemoveIp(requestedIp) {
				hasRequestedIp = true
			}
		}
		if hasRequestedIp {
			returnIps(cont.serviceIps, meta.ingressIps)
			meta.ingressIps = nil
			meta.staticIngressIps = []net.IP{requestedIp}
			meta.requestedIp = requestedIp
		}
	} else if meta.requestedIp != nil {
		meta.requestedIp = nil
		returnIps(cont.staticServiceIps, meta.staticIngressIps)
		meta.staticIngressIps = nil
	}

	if len(meta.ingressIps) == 0 && len(meta.staticIngressIps) == 0 {
		ipv4, err := cont.serviceIps.V4.GetIp()
		if err != nil {
			logger.Error("No IP addresses available for service")
		} else {
			meta.ingressIps = []net.IP{ipv4}
		}
	}

	cont.indexMutex.Unlock()

	var newIngress []v1.LoadBalancerIngress
	for _, ip := range meta.ingressIps {
		newIngress = append(newIngress, v1.LoadBalancerIngress{IP: ip.String()})
	}
	for _, ip := range meta.staticIngressIps {
		newIngress = append(newIngress, v1.LoadBalancerIngress{IP: ip.String()})
	}

	if !reflect.DeepEqual(newIngress, service.Status.LoadBalancer.Ingress) {
		service.Status.LoadBalancer.Ingress = newIngress

		_, err := cont.updateServiceStatus(service)
		if err != nil {
			logger.Error("Failed to update service: ", err)
		} else {
			logger.WithFields(logrus.Fields{
				"status": service.Status.LoadBalancer.Ingress,
			}).Info("Updated service load balancer status")
		}
	}
}

func (cont *AciController) createServiceEndpoint() (*metadata.ServiceEndpoint, error) {
	ep := &metadata.ServiceEndpoint{}
	_, err := net.ParseMAC(ep.Mac)
	if err != nil {
		var mac net.HardwareAddr
		mac = make([]byte, 6)
		_, err := rand.Read(mac)
		if err != nil {
			return nil, err
		}

		mac[0] = (mac[0] & 254) | 2
		ep.Mac = mac.String()
	}

	ipv4, err := cont.nodeServiceIps.V4.GetIp()
	if err == nil {
		ep.Ipv4 = ipv4
	} else {
		ep.Ipv4 = nil
	}

	ipv6, err := cont.nodeServiceIps.V6.GetIp()
	if err == nil {
		ep.Ipv6 = ipv6
	} else {
		ep.Ipv6 = nil
	}

	if ep.Ipv4 == nil && ep.Ipv6 == nil {
		return nil, errors.New("No IP addresses available")
	}

	return ep, nil
}

func (cont *AciController) getNodesForService(key string,
	service *v1.Service) (nodeMap map[string]*metadata.ServiceEndpoint,
	nodes []string) {

	nodeMap = make(map[string]*metadata.ServiceEndpoint)

	cont.indexMutex.Lock()
	if meta, ok := cont.serviceMetaCache[key]; ok {
		for node, sep := range meta.nodeServiceEps {
			if _, fpok := cont.fabricPathForNode(node); fpok {
				nodeMap[node] = sep
			}
		}
	}
	cont.indexMutex.Unlock()

	for node, _ := range nodeMap {
		nodes = append(nodes, node)
	}
	sort.Strings(nodes)
	return
}

func (cont *AciController) handleServiceUpdate(service *v1.Service) bool {
	servicekey, err := cache.MetaNamespaceKeyFunc(service)
	if err != nil {
		serviceLogger(cont.log, service).
			Error("Could not create service key: ", err)
		return false
	}

	isLoadBalancer := service.Spec.Type == v1.ServiceTypeLoadBalancer
	if isLoadBalancer {
		cont.indexMutex.Lock()
		if _, ok := cont.serviceMetaCache[servicekey]; !ok {
			cont.serviceMetaCache[servicekey] = &serviceMeta{
				nodeServiceEps: make(map[string]*metadata.ServiceEndpoint),
			}
		}
		cont.indexMutex.Unlock()

		if *cont.config.AllocateServiceIps {
			cont.allocateServiceIps(servicekey, service)
		}
		cont.allocateNodeServiceEps(servicekey, service)

		cont.indexMutex.Lock()
		if cont.serviceSyncEnabled {
			cont.indexMutex.Unlock()
			cont.updateServiceDeviceInstance(servicekey, service)
		} else {
			cont.indexMutex.Unlock()
		}
	} else {
		cont.clearLbService(servicekey)
	}

	cont.writeApicSvc(servicekey, service)

	return false
}

func (cont *AciController) clearLbService(servicekey string) {
	cont.indexMutex.Lock()
	if meta, ok := cont.serviceMetaCache[servicekey]; ok {
		returnIps(cont.serviceIps, meta.ingressIps)
		returnIps(cont.staticServiceIps, meta.staticIngressIps)
		for _, ep := range meta.nodeServiceEps {
			returnServiceEp(cont.nodeServiceIps, ep)
		}
		delete(cont.serviceMetaCache, servicekey)
	}
	cont.indexMutex.Unlock()
	cont.apicConn.ClearApicObjects(cont.aciNameForKey("svc", servicekey))
}

func (cont *AciController) serviceDeleted(obj interface{}) {
	service := obj.(*v1.Service)
	servicekey, err := cache.MetaNamespaceKeyFunc(service)
	if err != nil {
		serviceLogger(cont.log, service).
			Error("Could not create service key: ", err)
		return
	}
	cont.clearLbService(servicekey)
	cont.apicConn.ClearApicObjects(cont.aciNameForKey("service-vmm",
		servicekey))
}
