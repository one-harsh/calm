// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package middleware_test

import (
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/one-harsh/calm/internal/api/genapi"
	"github.com/one-harsh/calm/internal/server/middleware"
)

func TestExtractSessionTokenRoutes_RealSpec(t *testing.T) {
	spec, err := genapi.GetSpec()
	if err != nil {
		t.Fatalf("load spec: %v", err)
	}

	routes, err := middleware.ExtractSessionTokenRoutes(spec)
	if err != nil {
		t.Fatalf("ExtractSessionTokenRoutes: %v", err)
	}

	want := []string{
		"DELETE /v1/sessions",
		"POST /v1/events",
		"GET /v1/events",
		"GET /v1/sources",
		"GET /v1/snapshot",
		"POST /v1/ingest",
		"POST /v1/search",
	}
	for _, key := range want {
		method, path := splitRouteKey(key)
		if !routes.Has(method, path) {
			t.Errorf("routes missing %q", key)
		}
	}
	if len(routes) != len(want) {
		t.Errorf("routes has %d entries; want %d (extras: routes=%v)", len(routes), len(want), routes)
	}
}

func TestExtractSessionTokenRoutes_PathParamFailsStartup(t *testing.T) {
	loader := openapi3.NewLoader()
	spec, err := loader.LoadFromData([]byte(`
openapi: 3.0.3
info: {title: t, version: "1"}
paths:
  /v1/manage/clients/{client}:
    delete:
      operationId: deleteClientSession
      parameters:
        - in: path
          name: client
          required: true
          schema: { type: string }
        - in: header
          name: X-CALM-Session-Token
          required: true
          schema: { type: string, minLength: 1 }
      responses:
        '200': { description: ok }
`))
	if err != nil {
		t.Fatalf("load synthetic spec: %v", err)
	}

	_, err = middleware.ExtractSessionTokenRoutes(spec)
	if err == nil {
		t.Fatal("want error for session-keyed path-param route; got nil")
	}
	if !strings.Contains(err.Error(), "path parameter") {
		t.Errorf("err = %q; want substring \"path parameter\"", err.Error())
	}
}

func TestExtractSessionTokenRoutes_NoSessionRoutesEmpty(t *testing.T) {
	loader := openapi3.NewLoader()
	spec, err := loader.LoadFromData([]byte(`
openapi: 3.0.3
info: {title: t, version: "1"}
paths:
  /v1/health:
    get:
      operationId: getHealth
      responses:
        '200': { description: ok }
`))
	if err != nil {
		t.Fatalf("load synthetic spec: %v", err)
	}

	routes, err := middleware.ExtractSessionTokenRoutes(spec)
	if err != nil {
		t.Fatalf("ExtractSessionTokenRoutes: %v", err)
	}
	if len(routes) != 0 {
		t.Errorf("routes = %v; want empty", routes)
	}
}

func splitRouteKey(key string) (method, path string) {
	parts := strings.SplitN(key, " ", 2)
	return parts[0], parts[1]
}
