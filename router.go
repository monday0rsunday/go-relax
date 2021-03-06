// Copyright 2014-present Codehack. All rights reserved.
// For mobile and web development visit http://codehack.com
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package relax

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

/*
Router defines the routing system. Objects that implement it have functions
that add routes, find a handle to resources and provide information about routes.

Relax's default router is trieRegexpRouter. It takes full routes, with HTTP method and path, and
inserts them in a trie that can use regular expressions to match individual path segments.

PSE: trieRegexpRouter's path segment expressions (PSE) are match strings that are pre-compiled as
regular expressions. PSE's provide a simple layer of security when accepting values from
the path. Each PSE is made out of a {type:varname} format, where type is the expected type
for a value and varname is the name to give the variable that matches the value.

	"{word:varname}" // matches any word; alphanumeric and underscore.

	"{uint:varname}" // matches an unsigned integer.

	"{int:varname}" // matches a signed integer.

	"{float:varname}" // matches a floating-point number in decimal notation.

	"{date:varname}" // matches a date in ISO 8601 format.

	"{geo:varname}" // matches a geo location as described in RFC 5870

	"{hex:varname}" // matches a hex number, with optional "0x" prefix.

	"{uuid:varname}" // matches an UUID.

	"{varname}" // catch-all; matches anything. it may overlap other matches.

	"*" // translated into "{wild}"

	"{re:pattern}" // custom regexp pattern.

Some sample routes supported by trieRegexpRouter:

	GET /api/users/@{word:name}

	GET /api/users/{uint:id}/*

	POST /api/users/{uint:id}/profile

	DELETE /api/users/{date:from}/to/{date:to}

	GET /api/cities/{geo:location}

	PUT /api/investments/\${float:dollars}/fund

	GET /api/todos/month/{re:([0][1-9]|[1][0-2])}

Since PSE's are compiled to regexp, care must be taken to escape characters that
might break the compilation.
*/
type Router interface {
	// FindHandler should match request parameters to an existing resource handler and
	// return it. If no match is found, it should return an StatusError error which will
	// be sent to the requester. The default errors ErrRouteNotFound and
	// ErrRouteBadMethod cover the default cases.
	FindHandler(string, string, *url.Values) (HandlerFunc, error)

	// AddRoute is used to create new routes to resources. It expects the HTTP method
	// (GET, POST, ...) followed by the resource path and the handler function.
	AddRoute(string, string, HandlerFunc)

	// PathMethods returns a comma-separated list of HTTP methods that are matched
	// to a path. It will do PSE expansion.
	PathMethods(string) string
}

// These are errors returned by the default routing engine. You are encouraged to
// reuse them with your own Router.
var (
	// ErrRouteNotFound is returned when the path searched didn't reach a resource handler.
	ErrRouteNotFound = &StatusError{http.StatusNotFound, "That route was not found.", nil}

	// ErrRouteBadMethod is returned when the path did not match a given HTTP method.
	ErrRouteBadMethod = &StatusError{http.StatusMethodNotAllowed, "That method is not supported", nil}
)

// pathRegexpCache is a cache of all compiled regexp's so they can be reused.
var pathRegexpCache = make(map[string]*regexp.Regexp, 0)

// trieRegexpRouter implements Router with a trie that can store regular expressions.
// root points to the top of the tree from which all routes are searched and matched.
// methods is a list of all the methods used in routes.
type trieRegexpRouter struct {
	root    *trieNode
	methods []string
}

// trieNode contains the routing information.
// handler, if not nil, points to the resource handler served by a specific route.
// numExp is non-zero if the current path segment has regexp links.
// depth is the path depth of the current segment; 0 == HTTP verb.
// links are the contiguous path segments.
//
// For example, given the following route and handler:
//		"GET /api/users/111" -> users.GetUser()
//        - the path segment links are ["GET", "api", "users", "111"]
//        - "GET" has depth=0 and "111" has depth=3
//        - suppose "111" might be matched via regexp, then "users".numExp > 0
//        - "111" segment will point to the handler users.GetUser()
type trieNode struct {
	pseg    string
	handler HandlerFunc
	numExp  int
	depth   int
	links   []*trieNode
}

func (n *trieNode) findLink(pseg string) *trieNode {
	for i := range n.links {
		if n.links[i].pseg == pseg {
			return n.links[i]
		}
	}
	return nil
}

// segmentExp compiles the pattern string into a regexp so it can used in a
// path segment match. This function will panic if the regexp compilation fails.
func segmentExp(pattern string) *regexp.Regexp {
	// custom regexp pattern.
	if strings.HasPrefix(pattern, "{re:") {
		return regexp.MustCompile(pattern[4 : len(pattern)-1])
	}

	// turn "*" => "{wild}"
	pattern = strings.Replace(pattern, "*", `{wild}`, -1)
	// any: catch-all pattern
	p := regexp.MustCompile(`\{\w+\}`).
		ReplaceAllStringFunc(pattern, func(m string) string {
			return fmt.Sprintf(`(?P<%s>.+)`, m[1:len(m)-1])
		})
	// word: matches an alphanumeric word, with underscores.
	p = regexp.MustCompile(`\{(?:word\:)\w+\}`).
		ReplaceAllStringFunc(p, func(m string) string {
			return fmt.Sprintf(`(?P<%s>\w+)`, m[6:len(m)-1])
		})
	// date: matches a date as described in ISO 8601. see: https://en.wikipedia.org/wiki/ISO_8601
	// accepted values:
	// 	YYYY
	// 	YYYY-MM
	// 	YYYY-MM-DD
	// 	YYYY-MM-DDTHH
	// 	YYYY-MM-DDTHH:MM
	// 	YYYY-MM-DDTHH:MM:SS[.NN]
	// 	YYYY-MM-DDTHH:MM:SS[.NN]Z
	// 	YYYY-MM-DDTHH:MM:SS[.NN][+-]HH
	// 	YYYY-MM-DDTHH:MM:SS[.NN][+-]HH:MM
	//
	p = regexp.MustCompile(`\{(?:date\:)\w+\}`).
		ReplaceAllStringFunc(p, func(m string) string {
			name := m[6 : len(m)-1]
			return fmt.Sprintf(`(?P<%[1]s>(`+
				`(?P<%[1]s_year>\d{4})([/-]?`+
				`(?P<%[1]s_mon>(0[1-9])|(1[012]))([/-]?`+
				`(?P<%[1]s_mday>(0[1-9])|([12]\d)|(3[01])))?)?`+
				`(?:T(?P<%[1]s_hour>([01][0-9])|(?:2[0123]))(\:?(?P<%[1]s_min>[0-5][0-9])(\:?(?P<%[1]s_sec>[0-5][0-9]([\,\.]\d{1,10})?))?)?(?:Z|([\-+](?:([01][0-9])|(?:2[0123]))(\:?(?:[0-5][0-9]))?))?)?`+
				`))`, name)
		})
	// geo: geo location in decimal. See http://tools.ietf.org/html/rfc5870
	// accepted values:
	// 	lat,lon           (point)
	// 	lat,lon,alt       (3d point)
	// 	lag,lon;u=unc     (circle)
	// 	lat,lon,alt;u=unc (sphere)
	// 	lat,lon;crs=name  (point with coordinate reference system (CRS) value)
	p = regexp.MustCompile(`\{(?:geo\:)\w+\}`).ReplaceAllStringFunc(p, func(m string) string {
		name := m[5 : len(m)-1]
		return fmt.Sprintf(`(?P<%[1]s_lat>\-?\d+(\.\d+)?)[,;]`+
			`(?P<%[1]s_lon>\-?\d+(\.\d+)?)([,;]`+
			`(?P<%[1]s_alt>\-?\d+(\.\d+)?))?(((?:;crs=)`+
			`(?P<%[1]s_crs>[\w\-]+))?((?:;u=)`+
			`(?P<%[1]s_u>\-?\d+(\.\d+)?))?)?`, name)
	})
	// hex: matches a hexadecimal number.
	// accepted value: 0xNN
	p = regexp.MustCompile(`\{(?:hex\:)\w+\}`).
		ReplaceAllStringFunc(p, func(m string) string {
			return fmt.Sprintf(`(?P<%s>(?:0x)?[[:xdigit:]]+)`, m[5:len(m)-1])
		})
	// uuid: matches an UUID using hex octets, with optional dashes.
	// accepted value: NNNNNNNN-NNNN-NNNN-NNNN-NNNNNNNNNNNN
	p = regexp.MustCompile(`\{(?:uuid\:)\w+\}`).
		ReplaceAllStringFunc(p, func(m string) string {
			return fmt.Sprintf(`(?P<%s>[[:xdigit:]]{8}\-?`+
				`[[:xdigit:]]{4}\-?`+
				`[[:xdigit:]]{4}\-?`+
				`[[:xdigit:]]{4}\-?`+
				`[[:xdigit:]]{12})`, m[6:len(m)-1])
		})
	// float: matches a floating-point number
	p = regexp.MustCompile(`\{(?:float\:)\w+\}`).
		ReplaceAllStringFunc(p, func(m string) string {
			return fmt.Sprintf(`(?P<%s>[\-+]?\d+\.\d+)`, m[7:len(m)-1])
		})
	// uint: matches an unsigned integer number (64bit)
	p = regexp.MustCompile(`\{(?:uint\:)\w+\}`).
		ReplaceAllStringFunc(p, func(m string) string {
			return fmt.Sprintf(`(?P<%s>\d{1,18})`, m[6:len(m)-1])
		})
	// int: matches a signed integer number (64bit)
	p = regexp.MustCompile(`\{(?:int\:)\w+\}`).
		ReplaceAllStringFunc(p, func(m string) string {
			return fmt.Sprintf(`(?P<%s>[-+]?\d{1,18})`, m[5:len(m)-1])
		})
	return regexp.MustCompile(p)
}

// AddRoute breaks a path into segments and inserts them in the tree. If a
// segment contains matching {}'s then it is tried as a regexp segment, otherwise it is
// treated as a regular string segment.
func (router *trieRegexpRouter) AddRoute(method, path string, handler HandlerFunc) {
	node := router.root
	pseg := strings.Split(method+strings.TrimRight(path, "/"), "/")
	for i := range pseg {
		if (strings.Contains(pseg[i], "{") && strings.Contains(pseg[i], "}")) || strings.Contains(pseg[i], "*") {
			if _, ok := pathRegexpCache[pseg[i]]; !ok {
				pathRegexpCache[pseg[i]] = segmentExp(pseg[i])
			}
			node.numExp++
		}
		link := node.findLink(pseg[i])
		if link == nil {
			link = &trieNode{
				pseg:  pseg[i],
				depth: node.depth + 1,
			}
			node.links = append(node.links, link)
		}
		node = link
	}

	node.handler = handler

	// update methods list
	if !strings.Contains(strings.Join(router.methods, ","), method) {
		router.methods = append(router.methods, method)
	}
}

// matchSegment tries to match a path segment 'pseg' to the node's regexp links.
// This function will return any path values matched so they can be used in
// Request.PathValues.
func (node *trieNode) matchSegment(pseg string, depth int, values *url.Values) *trieNode {
	if node.numExp == 0 {
		return node.findLink(pseg)
	}
	for pexp := range node.links {
		rx := pathRegexpCache[node.links[pexp].pseg]
		if rx == nil {
			continue
		}
		// this prevents the matching to be side-tracked by smaller paths.
		if depth > node.links[pexp].depth && node.links[pexp].links == nil {
			continue
		}
		m := rx.FindStringSubmatch(pseg)
		if len(m) > 1 && m[0] == pseg {
			if values != nil {
				if *values == nil {
					*values = make(url.Values)
				}
				sub := rx.SubexpNames()
				for i, n := 1, len(*values)/2; i < len(m); i++ {
					_n := fmt.Sprintf("_%d", n+i)
					(*values).Set(_n, m[i])
					if sub[i] != "" {
						(*values).Add(sub[i], m[i])
					}
				}
			}
			return node.links[pexp]
		}
	}
	return node.findLink(pseg)
}

// FindHandler returns a resource handler that matches the requested route; or
// an error (StatusError) if none found.
// method is the HTTP verb.
// path is the relative URI path.
// values is a pointer to an url.Values map to store parameters from the path.
func (router *trieRegexpRouter) FindHandler(method, path string, values *url.Values) (HandlerFunc, error) {
	if method == "HEAD" {
		method = "GET"
	}
	node := router.root
	pseg := strings.Split(method+strings.TrimRight(path, "/"), "/") // ex: GET/api/users
	slen := len(pseg)
	for i := range make([]struct{}, slen) {
		if node == nil {
			if i <= 1 {
				return nil, ErrRouteBadMethod
			}
			return nil, ErrRouteNotFound
		}
		node = node.matchSegment(pseg[i], slen, values)
	}

	if node == nil || node.handler == nil {
		return nil, ErrRouteNotFound
	}
	return node.handler, nil
}

// PathMethods returns a string with comma-separated HTTP methods that match
// the path. This list is suitable for Allow header response. Note that this
// function only lists the methods, not if they are allowed.
func (router *trieRegexpRouter) PathMethods(path string) string {
	var node *trieNode
	methods := "HEAD" // cheat
	pseg := strings.Split("*"+strings.TrimRight(path, "/"), "/")
	slen := len(pseg)
	for _, method := range router.methods {
		node = router.root
		pseg[0] = method
		for i := range pseg {
			if node == nil {
				continue
			}
			node = node.matchSegment(pseg[i], slen, nil)
		}
		if node == nil || node.handler == nil {
			continue
		}
		methods += ", " + method
	}
	return methods
}

// newRouter returns a new trieRegexpRouter object with an initialized tree.
func newRouter() *trieRegexpRouter {
	return &trieRegexpRouter{root: new(trieNode)}
}
