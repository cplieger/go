package httpwire_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"

	"github.com/cplieger/go/httpwire"
)

// exampleID is a path value of at most 32 bytes.
type exampleID string

func (exampleID) MaxBytes() int { return 32 }

type exampleRef struct {
	ID exampleID `json:"id"`
}

type exampleItem struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// itemDoc is the OpenAPI document wiregen writes for the table below; an app
// commits it and embeds it.
const itemDoc = `{
  "openapi": "3.1.0",
  "info": {"title": "items", "version": "1"},
  "paths": {
    "/api/items/{id}": {
      "get": {
        "operationId": "items.get",
        "summary": "Read an item",
        "parameters": [
          {"in": "path", "name": "id", "required": true, "schema": {"type": "string", "minLength": 1, "maxLength": 32}}
        ],
        "responses": {
          "200": {
            "description": "OK",
            "content": {"application/json": {"schema": {
              "type": "object",
              "properties": {"id": {"type": "string"}, "name": {"type": "string"}},
              "required": ["id", "name"]
            }}}
          }
        }
      }
    }
  }
}`

// A table row and its typed handler: the binder decodes the path value into
// its type, and a value outside its bounds names no resource, so it answers
// not_found without running the handler.
func ExampleNew() {
	table := &httpwire.Table{
		Prefix:  "/api",
		Catalog: httpwire.NewCatalog(),
		Endpoints: []httpwire.Endpoint{{
			Name: "items.get", Method: http.MethodGet, Path: "/api/items/{id}",
			Summary: "Read an item", Description: "Reads one item.",
			Params: reflect.TypeFor[exampleRef](), Response: reflect.TypeFor[exampleItem](),
			Access: httpwire.Access{Class: httpwire.Public},
		}},
	}
	get := func(_ context.Context, c httpwire.Call[exampleRef, httpwire.None, httpwire.None, httpwire.None]) (exampleItem, error) {
		return exampleItem{ID: string(c.Params.ID), Name: "first"}, nil
	}
	api, err := httpwire.New(table, []httpwire.Binding{httpwire.Bind("items.get", get)},
		httpwire.WithSchema([]byte(itemDoc)))
	if err != nil {
		fmt.Println(err)
		return
	}

	for _, target := range []string{"/api/items/a1", "/api/items/" + strings.Repeat("x", 33)} {
		rec := httptest.NewRecorder()
		api.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, http.NoBody))
		env, _ := httpwire.DecodeEnvelope(rec.Body.Bytes())
		fmt.Println(rec.Code, env.Code, rec.Header().Get("Wire-Version") != "")
	}
	// Output:
	// 200  true
	// 404 not_found true
}
