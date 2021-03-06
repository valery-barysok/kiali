// Package options holds the option settings for a single graph generation.
package options

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/prometheus/common/model"

	"github.com/kiali/kiali/business"
	"github.com/kiali/kiali/graph"
	"github.com/kiali/kiali/graph/appender"
)

const (
	GroupByApp                string = "app"
	GroupByNone               string = "none"
	GroupByVersion            string = "version"
	NamespaceIstio            string = "istio-system"
	VendorCytoscape           string = "cytoscape"
	defaultDuration           string = "10m"
	defaultGraphType          string = graph.GraphTypeWorkload
	defaultGroupBy            string = GroupByNone
	defaultIncludeIstio       bool   = false
	defaultInjectServiceNodes bool   = false
	defaultVendor             string = VendorCytoscape
)

const (
	graphKindNamespace string = "namespace"
	graphKindNode      string = "node"
)

// NodeOptions are those that apply only to node-detail graphs
type NodeOptions struct {
	App       string
	Namespace string
	Service   string
	Version   string
	Workload  string
}

// VendorOptions are those that are supplied to the vendor-specific generators.
type VendorOptions struct {
	Duration  time.Duration
	GraphType string
	GroupBy   string
	QueryTime int64 // unix time in seconds
}

// Options are all supported graph generation options.
type Options struct {
	AccessibleNamespaces map[string]time.Time
	Appenders            []appender.Appender
	IncludeIstio         bool // include istio-system services. Ignored for istio-system ns. Default false.
	InjectServiceNodes   bool // inject destination service nodes between source and destination nodes.
	Namespaces           map[string]graph.NamespaceInfo
	Vendor               string
	NodeOptions
	VendorOptions
}

func NewOptions(r *http.Request) Options {
	// path variables (0 or more will be set)
	vars := mux.Vars(r)
	app := vars["app"]
	namespace := vars["namespace"]
	service := vars["service"]
	version := vars["version"]
	workload := vars["workload"]

	// query params
	params := r.URL.Query()
	var duration model.Duration
	var includeIstio bool
	var injectServiceNodes bool
	var queryTime int64
	durationString := params.Get("duration")
	graphType := params.Get("graphType")
	groupBy := params.Get("groupBy")
	includeIstioString := params.Get("includeIstio")
	injectServiceNodesString := params.Get("injectServiceNodes")
	namespaces := params.Get("namespaces") // csl of namespaces
	queryTimeString := params.Get("queryTime")
	vendor := params.Get("vendor")

	if durationString == "" {
		duration, _ = model.ParseDuration(defaultDuration)
	} else {
		var durationErr error
		duration, durationErr = model.ParseDuration(durationString)
		if durationErr != nil {
			graph.BadRequest(fmt.Sprintf("Invalid duration [%s]", durationString))
		}
	}
	if graphType == "" {
		graphType = defaultGraphType
	} else if graphType != graph.GraphTypeApp && graphType != graph.GraphTypeService && graphType != graph.GraphTypeVersionedApp && graphType != graph.GraphTypeWorkload {
		graph.BadRequest(fmt.Sprintf("Invalid graphType [%s]", graphType))
	}
	// app node graphs require an app graph type
	if app != "" && graphType != graph.GraphTypeApp && graphType != graph.GraphTypeVersionedApp {
		graph.BadRequest(fmt.Sprintf("Invalid graphType [%s]. This node detail graph supports only graphType app or versionedApp.", graphType))
	}
	if groupBy == "" {
		groupBy = defaultGroupBy
	} else if groupBy != GroupByApp && groupBy != GroupByNone && groupBy != GroupByVersion {
		graph.BadRequest(fmt.Sprintf("Invalid groupBy [%s]", groupBy))
	}
	if includeIstioString == "" {
		includeIstio = defaultIncludeIstio
	} else {
		var includeIstioErr error
		includeIstio, includeIstioErr = strconv.ParseBool(includeIstioString)
		if includeIstioErr != nil {
			graph.BadRequest(fmt.Sprintf("Invalid includeIstio [%s]", includeIstioString))
		}
	}
	if injectServiceNodesString == "" {
		injectServiceNodes = defaultInjectServiceNodes
	} else {
		var injectServiceNodesErr error
		injectServiceNodes, injectServiceNodesErr = strconv.ParseBool(injectServiceNodesString)
		if injectServiceNodesErr != nil {
			graph.BadRequest(fmt.Sprintf("Invalid injectServiceNodes [%s]", injectServiceNodesString))
		}
	}
	if queryTimeString == "" {
		queryTime = time.Now().Unix()
	} else {
		var queryTimeErr error
		queryTime, queryTimeErr = strconv.ParseInt(queryTimeString, 10, 64)
		if queryTimeErr != nil {
			graph.BadRequest(fmt.Sprintf("Invalid queryTime [%s]", queryTimeString))
		}
	}
	if vendor == "" {
		vendor = defaultVendor
	} else if vendor != VendorCytoscape {
		graph.BadRequest(fmt.Sprintf("Invalid vendor [%s]", vendor))
	}

	// Process namespaces options:
	namespaceMap := make(map[string]graph.NamespaceInfo)

	tokenContext := r.Context().Value("token")
	var token string
	if tokenContext != nil {
		if tokenString, ok := tokenContext.(string); !ok {
			graph.Error("token is not of type string")
		} else {
			token = tokenString
		}
	} else {
		graph.Error("token missing in request context")
	}

	accessibleNamespaces := getAccessibleNamespaces(token)

	// If path variable is set then it is the only relevant namespace (it's a node graph)
	// Else if namespaces query param is set it specifies the relevant namespaces
	// Else error, at least one namespace is required.
	if namespace != "" {
		namespaces = namespace
	}

	if namespaces == "" {
		graph.BadRequest(fmt.Sprintf("At least one namespace must be specified via the namespaces query parameter."))
	}

	for _, namespaceToken := range strings.Split(namespaces, ",") {
		namespaceToken = strings.TrimSpace(namespaceToken)
		if creationTime, found := accessibleNamespaces[namespaceToken]; found {
			namespaceMap[namespaceToken] = graph.NamespaceInfo{
				Name:     namespaceToken,
				Duration: resolveNamespaceDuration(creationTime, time.Duration(duration), queryTime),
			}
		} else {
			graph.Forbidden(fmt.Sprintf("Requested namespace [%s] is not accessible.", namespaceToken))
		}
	}

	// Service graphs require service injection
	if graphType == graph.GraphTypeService {
		injectServiceNodes = true
	}

	options := Options{
		AccessibleNamespaces: accessibleNamespaces,
		IncludeIstio:         includeIstio,
		InjectServiceNodes:   injectServiceNodes,
		Namespaces:           namespaceMap,
		Vendor:               vendor,
		NodeOptions: NodeOptions{
			App:       app,
			Namespace: namespace,
			Service:   service,
			Version:   version,
			Workload:  workload,
		},
		VendorOptions: VendorOptions{
			Duration:  time.Duration(duration),
			GraphType: graphType,
			GroupBy:   groupBy,
			QueryTime: queryTime,
		},
	}

	appenders := parseAppenders(params, options)
	options.Appenders = appenders

	return options
}

// GetGraphKind will return the kind of graph represented by the options.
func (o *Options) GetGraphKind() string {
	if o.NodeOptions.App != "" ||
		o.NodeOptions.Version != "" ||
		o.NodeOptions.Workload != "" ||
		o.NodeOptions.Service != "" {
		return graphKindNode
	} else {
		return graphKindNamespace
	}
}

func parseAppenders(params url.Values, o Options) []appender.Appender {
	requestedAppenders := make(map[string]bool)
	allAppenders := false
	if _, ok := params["appenders"]; ok {
		for _, requestedAppender := range strings.Split(params.Get("appenders"), ",") {
			switch strings.TrimSpace(requestedAppender) {
			case appender.DeadNodeAppenderName:
				requestedAppenders[appender.DeadNodeAppenderName] = true
			case appender.ServiceEntryAppenderName:
				requestedAppenders[appender.ServiceEntryAppenderName] = true
			case appender.IstioAppenderName:
				requestedAppenders[appender.IstioAppenderName] = true
			case appender.ResponseTimeAppenderName:
				requestedAppenders[appender.ResponseTimeAppenderName] = true
			case appender.SecurityPolicyAppenderName:
				requestedAppenders[appender.SecurityPolicyAppenderName] = true
			case appender.SidecarsCheckAppenderName:
				requestedAppenders[appender.SidecarsCheckAppenderName] = true
			case appender.UnusedNodeAppenderName:
				requestedAppenders[appender.UnusedNodeAppenderName] = true
			case "":
				// skip
			default:
				graph.BadRequest(fmt.Sprintf("Invalid appender [%s]", strings.TrimSpace(requestedAppender)))
			}
		}
	} else {
		allAppenders = true
	}

	// The appender order is important
	// To pre-process service nodes run service_entry appender first
	// To reduce processing, filter dead nodes next
	// To reduce processing, next run appenders that don't apply to unused services
	// Add orphan (unused) services
	// Run remaining appenders
	var appenders []appender.Appender

	if _, ok := requestedAppenders[appender.ServiceEntryAppenderName]; ok || allAppenders {
		a := appender.ServiceEntryAppender{
			AccessibleNamespaces: o.AccessibleNamespaces,
		}
		appenders = append(appenders, a)
	}
	if _, ok := requestedAppenders[appender.DeadNodeAppenderName]; ok || allAppenders {
		a := appender.DeadNodeAppender{}
		appenders = append(appenders, a)
	}
	if _, ok := requestedAppenders[appender.ResponseTimeAppenderName]; ok || allAppenders {
		quantile := appender.DefaultQuantile
		if _, ok := params["responseTimeQuantile"]; ok {
			if responseTimeQuantile, err := strconv.ParseFloat(params.Get("responseTimeQuantile"), 64); err == nil {
				quantile = responseTimeQuantile
			}
		}
		a := appender.ResponseTimeAppender{
			Quantile:           quantile,
			GraphType:          o.GraphType,
			InjectServiceNodes: o.InjectServiceNodes,
			IncludeIstio:       o.IncludeIstio,
			Namespaces:         o.Namespaces,
			QueryTime:          o.QueryTime,
		}
		appenders = append(appenders, a)
	}
	if _, ok := requestedAppenders[appender.SecurityPolicyAppenderName]; ok || allAppenders {
		a := appender.SecurityPolicyAppender{
			GraphType:          o.GraphType,
			IncludeIstio:       o.IncludeIstio,
			InjectServiceNodes: o.InjectServiceNodes,
			Namespaces:         o.Namespaces,
			QueryTime:          o.QueryTime,
		}
		appenders = append(appenders, a)
	}
	if _, ok := requestedAppenders[appender.UnusedNodeAppenderName]; ok || allAppenders {
		hasNodeOptions := o.App != "" || o.Workload != "" || o.Service != ""
		a := appender.UnusedNodeAppender{
			GraphType:   o.GraphType,
			IsNodeGraph: hasNodeOptions,
		}
		appenders = append(appenders, a)
	}
	if _, ok := requestedAppenders[appender.IstioAppenderName]; ok || allAppenders {
		a := appender.IstioAppender{}
		appenders = append(appenders, a)
	}
	if _, ok := requestedAppenders[appender.SidecarsCheckAppenderName]; ok || allAppenders {
		a := appender.SidecarsCheckAppender{}
		appenders = append(appenders, a)
	}

	return appenders
}

// getAccessibleNamespaces returns a Set of all namespaces accessible to the user.
// The Set is implemented using the map convention. Each map entry is set to the
// creation timestamp of the namespace, to be used to ensure valid time ranges for
// queries against the namespace.
func getAccessibleNamespaces(token string) map[string]time.Time {
	// Get the namespaces
	business, err := business.Get(token)
	graph.CheckError(err)

	namespaces, err := business.Namespace.GetNamespaces()
	graph.CheckError(err)

	// Create a map to store the namespaces
	namespaceMap := make(map[string]time.Time)
	for _, namespace := range namespaces {
		namespaceMap[namespace.Name] = namespace.CreationTimestamp
	}

	return namespaceMap
}

// resolveNamespaceDuration determines if, given queryTime, the requestedRange won't lead to
// querying data before nsCreationTime. If this is the case, resolveNamespaceDuration returns
// and adjusted range. Else, the original requestedRange is returned.
func resolveNamespaceDuration(nsCreationTime time.Time, requestedRange time.Duration, queryTime int64) time.Duration {
	var referenceTime time.Time
	resolvedBound := requestedRange

	if !nsCreationTime.IsZero() {
		if queryTime != 0 {
			referenceTime = time.Unix(queryTime, 0)
		} else {
			referenceTime = time.Now()
		}

		nsLifetime := referenceTime.Sub(nsCreationTime)
		if nsLifetime < resolvedBound {
			resolvedBound = nsLifetime
		}
	}

	return resolvedBound
}
