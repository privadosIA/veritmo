package api_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	"veritmo/internal/server/api"
)

// Every error of the API has this body: a machine-readable code and a message for a person. The status of the response
// says the class of the error; the code says which one.
func ExampleWriteError() {
	rec := httptest.NewRecorder()
	api.WriteError(rec, http.StatusNotFound, api.CodeNotFound, `project "demo" not found`)

	fmt.Println(rec.Code, rec.Header().Get("Content-Type"))
	fmt.Print(strings.TrimSpace(rec.Body.String()), "\n")
	// Output:
	// 404 application/json
	// {"error":{"code":"not_found","message":"project \"demo\" not found"}}
}
