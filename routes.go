package mrnes

// routes.go provides functions to create and access shortest path routes through a MrNesbit network

import (
	"fmt"
	"golang.org/x/exp/slices"
	"gonum.org/v1/gonum/graph"
	"gonum.org/v1/gonum/graph/path"
	"gonum.org/v1/gonum/graph/simple"
	"math"
	"strconv"
	"strings"
)

// The general approach we use is to convert a MrNesBits representation of the network
// into the data structures used by a graph package that has built-in path discovery algorithms.
// Weighting each edge by 1, a shortest path minimizes the number of hops, which is sort of what
// local routing like OSPF does.
//   The network representation from MrNesbits represents devices (Host, Switch, Router), with
// a link from devA to devB being represented if there is a direct wired or wireless connection
// between them.  After a path is computed from device to device to device ... we discover the identity
// of the interface through which the path accesses the device, and report that as part of the path.
//
//   The Dijsktra algorithm we call computes a tree of shortest paths from a named node.
// so then if we want the shortest path from src to dst, we either compute such a tree rooted in
// src, or look up from a cached version of an already computed tree the sequence of nodes
// between src and dst, inclusive. Failing that we look for a known path from dst to src, which
// will by symmetry be the reversed path of what we want.

// ShowPath returns a string that lists the names of all the network devices on a
// path. The input arguments are the source and destination device ids, a dictionary holding string
// names as a function of device id, representation the path, and a flag indicating whether the path
// is from src to dst, or vice-versa
func ShowPath(src int, dest int, idToName map[int]string, thru map[int]int,
	forwardDirection bool) string {

	// sequence will hold the names of the devices on the path, in the reverse order they are visited
	sequence := make([]string, 0)
	here := dest

	// the 'thru' map identifies the point-to-point connections on the path, in reverse order
	// (a result of the algorithm used to discover the path).
	// Loop until the path discovered backwards takes us to the source
	for here != src {
		sequence = append(sequence, idToName[here])
		here = thru[here]
	}
	sequence = append(sequence, idToName[src])

	// put the sequence of device names into the pathString list. If input
	// argument forwardDirection is true then the order we want in sequence is in reverse
	// order in sequence.
	pathString := make([]string, 0)
	if forwardDirection {
		for idx := len(sequence) - 1; idx > -1; idx-- {
			pathString = append(pathString, sequence[idx])
		}
	} else {
		pathString = append(pathString, sequence...)
	}

	return strings.Join(pathString, ",")
}

// the gNodes data structure implements a graph representation of the MrNesbits network
// in a form that let's us use the graph module.
// gNodes[i] refers to the MrNesbits network device with id i
var gNodes map[int]simple.Node = make(map[int]simple.Node)

// buildconnGraph returns a graph.Graph data structure built from
// a MrNesbits representation of a node id and a list of node ids of
// network devices it connects to.
func buildconnGraph(edges map[int][]int) graph.Graph {
	connGraph := simple.NewWeightedUndirectedGraph(0, math.Inf(1))
	for nodeId := range edges {
		_, present := gNodes[nodeId]
		if present {
			continue
		}
		gNodes[nodeId] = simple.Node(nodeId)
	}

	// transform the expression of edges input list to edges in the graph module representation

	// MrNesbits device id and list of ids of edges it connects to
	for nodeId, edgeList := range edges {
		// for every neighbor in that list
		for _, nbrId := range edgeList {
			// represent the edge (with weight 1) in the form that the graph module represents it
			weightedEdge := simple.WeightedEdge{F: gNodes[nodeId], T: gNodes[nbrId], W: 1.0}
			connGraph.SetWeightedEdge(weightedEdge)
		}
	}
	// set the flag to show we've done it and so don't need to do it again
	connGraphBuilt = true

	return connGraph
}

// cachedSP saves the resultof computing shortest-path trees.
// The key is the device id of the path source, the value is the graph/path representation of the tree
var cachedSP map[int]path.Shortest = make(map[int]path.Shortest)

// getSPTree returns the shortest path tree rooted in input argument 'from' from
// tree 'connGraph'.  If the tree is found in the cache it is returned, if not it is computed, saved, and returned.
func getSPTree(from int, connGraph graph.Graph) path.Shortest {

	// look for existence of tree already
	spTree, present := cachedSP[from]
	if present {
		// yes, we're done
		return spTree
	}

	// let graph/path.DijkstraFrom compute the tree. The first argument
	// is the root of the tree, the second is the graph
	spTree = path.DijkstraFrom(gNodes[from], connGraph)

	// save (using the MrNesbits identity for the node) and return
	cachedSP[from] = spTree

	return spTree
}

// convertNodeSeq extracts the MrNesbits network device ids from a sequence of graph nodes
// (e.g. like a path) and returns that list
func convertNodeSeq(nsQ []graph.Node) []int {
	rtn := []int{}
	for _, node := range nsQ {
		nodeId, _ := strconv.Atoi(fmt.Sprintf("%d", node))
		rtn = append(rtn, nodeId)
	}

	return rtn
}

// connGraphBuilt is a flag which is initialized to false but is set true
// once the connection graph is constructed.
var connGraphBuilt bool = false

// connGraph is the path/graph representation of the MrNesbits network graph
var connGraph graph.Graph

// routeFrom returns the shortest path (as a sequence of network device identifiers)
// from the named source to the named destination, through the provided graph (in MrNesbits
// format of device ids)
func routeFrom(srcId int, edges map[int][]int, dstId int) []int {
	// make sure we've built the path/graph respresentation
	if !connGraphBuilt {
		// buildconnGraph creates the graph, and sets the connGraphBuilt flag
		connGraph = buildconnGraph(edges)
	}

	// nodeSeq holds the desired path expressed as a sequence of path/graph nodes
	var nodeSeq []graph.Node

	// route holds the desired path expressed as a sequence of MrNesbits device ids,
	// ultimately what routeFrom returns
	var route []int

	// if we have already an spTree rooted in srcId we can use it.
	spTree, present := cachedSP[srcId]

	if present {
		// get the path through the tree to the node with label dstId
		// (representing the MrNesbits device with that label)
		nodeSeq, _ = spTree.To(int64(dstId))

		// convert the sequence of graph/path nodes to a sequence of MrNesbits device ids
		route = convertNodeSeq(nodeSeq)
	} else {
		// it may be that we have already a shortest path tree that is routed in the destination.
		// if so, by symmetry the path is the same, just reversed.
		spTree, present = cachedSP[dstId]
		if present {
			// get the path from the graph node with label dstId to the graph node with label srcId
			revNodeSeq, _ := spTree.To(int64(srcId))

			// convert that sequence of graph nodes to a sequence of MrNesbits device ids
			revRoute := convertNodeSeq(revNodeSeq)

			// these are reverse order, so turn them back around
			lenR := len(revRoute)
			for idx := 0; idx < lenR; idx++ {
				route = append(route, revRoute[lenR-idx-1])
			}
		} else {
			// we don't have a tree routed in either srcId or dstId, so make a tree rooted in srcId
			spTree = getSPTree(srcId, connGraph)

			// get the path as a sequence of graph nodes, convert to a sequence of MrNesbits device id values
			nodeSeq, _ = spTree.To(int64(dstId))
			route = convertNodeSeq(nodeSeq)
		}
	}

	return route
}

type rtEndpts struct {
	srcId, dstId int
}

func networkBetween(devA, devB topoDev) string {
	facedByA := []string{}

	for _, intrfc := range devA.devIntrfcs() {
		facedByA = append(facedByA, intrfc.faces.name)
	}
	for _, intrfc := range devB.devIntrfcs() {
		if slices.Contains(facedByA, intrfc.faces.name) {
			return intrfc.faces.name
		}
	}

	return ""
}

// commonNetId checks that intrfcA and intrfcB point at the same network and returns its name
func commonNetId(intrfcA, intrfcB *intrfcStruct) int {
	if intrfcA.faces == nil || intrfcB.faces == nil {
		panic("interface on route fails to face any network")
	}

	if intrfcA.faces.name != intrfcB.faces.name {
		panic("interfaces on route do not face the same network")
	}
	return intrfcA.faces.number
}


// compute identify of the interfaces between routeSteps rsA and rsB
func intrfcsBetween(rsA, rsB int) (int, int) {
	return getRouteStepIntrfcs(rsA, rsB)
}
/*
	devA := topoDevById[rsA]
	devB := topoDevById[rsB]

	idA := devA.devId()
	idB := devB.devId()

	var intrfcA int = -1
	var intrfcB int = -1

	// find the name of the network between devA and devB
	netName := networkBetween(devA, devB)
	if netName == "" {
		panic("expected network name")
	}

	// find the interface on rsA that connects to rsB
	for _, intrfc := range devA.devIntrfcs() {
		if intrfc.isConnected() && topoDevByName[intrfc.connects.device.devName()].devId() == idB {
			intrfcA = intrfc.number
			break
		}
		if !intrfc.isConnected() && intrfc.faces.name == netName {
			intrfcA = intrfc.number
			break
		}
	}

	// find the interface on rsB that connects to rsA
	for _, intrfc := range devB.devIntrfcs() {
		if intrfc.isConnected() && topoDevByName[intrfc.connects.device.devName()].devId() == idA {
			intrfcB = intrfc.number
			break
		}
		if !intrfc.isConnected() && intrfc.faces.name == netName {
			intrfcB = intrfc.number
			break
		}
	}

	if intrfcA != -1 && intrfcB != -1 {
		return intrfcA, intrfcB
	}

	fmt.Printf("duplex connection not found between devices %s and %s\n",
		topoDevById[rsA].devName(), topoDevById[rsB].devName())

	return -1, -1
}
*/

var pcktRtCache map[rtEndpts]*[]intrfcsToDev = make(map[rtEndpts]*[]intrfcsToDev)

func findRoute(srcId, dstId int) *[]intrfcsToDev {
	endpoints := rtEndpts{srcId: srcId, dstId: dstId}

	rt, found := pcktRtCache[endpoints]
	if found {
		return rt
	}

	route := routeFrom(srcId, topoGraph, dstId)
	routePlan := make([]intrfcsToDev, 0)

	for idx := 1; idx < len(route); idx++ {
		devId := route[idx]

		srcIntrfcId, dstIntrfcId := intrfcsBetween(route[idx-1], devId)

		networkId := -1
		dstIntrfc := intrfcById[dstIntrfcId]

		// if 'cable' is nil we're pointing through a network and
		// use its id
		if dstIntrfc.cable == nil {
			networkId = dstIntrfc.faces.number
		}

		istp := intrfcsToDev{srcIntrfcId: srcIntrfcId, dstIntrfcId: dstIntrfcId, netId: networkId, devId: devId}
		routePlan = append(routePlan, istp)
	}
	pcktRtCache[endpoints] = &routePlan

	return &routePlan
}
