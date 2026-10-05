package server

import (
	"errors"
	"maps"
	"net/http"

	"github.com/melounvitek/gripi/internal/environment"
)

func (app *application) registerEnvironmentRoutes(mux *http.ServeMux) {
	mux.Handle("GET /environment", noStore(http.HandlerFunc(app.environmentVariables)))
	mux.Handle("GET /environment/value", noStore(http.HandlerFunc(app.environmentValue)))
	mux.Handle("POST /environment/variable", noStore(http.HandlerFunc(app.saveEnvironmentVariable)))
	mux.Handle("POST /environment/variables", noStore(http.HandlerFunc(app.saveEnvironmentVariables)))
	mux.Handle("POST /environment/variable/delete", noStore(http.HandlerFunc(app.deleteEnvironmentVariable)))
}

// environmentUser is the user whose variables a request reads and changes.
func (app *application) environmentUser(request *http.Request) string {
	if app.config.MultiUserMode {
		return currentWorkspaceID(request)
	}
	return ""
}

// piEnvironment returns what Pi gets on top of the gateway's environment for a session of the user.
func (app *application) piEnvironment(userID string) ([]string, error) {
	// A multi-user session without an owner must not get the variables saved in single-user mode.
	if app.config.MultiUserMode && userID == "" {
		return nil, nil
	}
	variables, err := app.environment.Variables(userID)
	if err != nil {
		logInternalError("read environment variables for Pi", err)
		return nil, err
	}
	entries := make([]string, 0, len(variables))
	for _, variable := range variables {
		entries = append(entries, variable.Name+"="+variable.Value)
	}
	return entries, nil
}

func (app *application) environmentVariables(response http.ResponseWriter, request *http.Request) {
	variables, err := app.environment.Variables(app.environmentUser(request))
	writeEnvironmentVariables(response, variables, nil, err)
}

func (app *application) environmentValue(response http.ResponseWriter, request *http.Request) {
	variables, err := app.environment.Variables(app.environmentUser(request))
	if err != nil {
		writeEnvironmentError(response, err)
		return
	}
	for _, variable := range variables {
		if variable.Name == request.URL.Query().Get("name") {
			writeJSON(response, map[string]string{"value": variable.Value})
			return
		}
	}
	http.NotFound(response, request)
}

func (app *application) saveEnvironmentVariable(response http.ResponseWriter, request *http.Request) {
	if !parseForm(response, request) {
		return
	}
	variable := environment.Variable{Name: request.FormValue("name"), Value: request.FormValue("value")}
	variables, err := app.environment.Save(app.environmentUser(request), request.FormValue("previous_name"), variable)
	writeEnvironmentVariables(response, variables, nil, err)
}

func (app *application) saveEnvironmentVariables(response http.ResponseWriter, request *http.Request) {
	if !parseForm(response, request) {
		return
	}
	variables, saved, err := app.environment.SaveBlock(app.environmentUser(request), request.FormValue("text"))
	writeEnvironmentVariables(response, variables, map[string]any{"saved": saved}, err)
}

func (app *application) deleteEnvironmentVariable(response http.ResponseWriter, request *http.Request) {
	if !parseForm(response, request) {
		return
	}
	variables, err := app.environment.Delete(app.environmentUser(request), request.FormValue("name"))
	writeEnvironmentVariables(response, variables, nil, err)
}

// writeEnvironmentVariables answers with the names in saved order, never the values, and the extra fields.
func writeEnvironmentVariables(response http.ResponseWriter, variables []environment.Variable, extra map[string]any, err error) {
	if err != nil {
		writeEnvironmentError(response, err)
		return
	}
	names := make([]map[string]string, 0, len(variables))
	for _, variable := range variables {
		names = append(names, map[string]string{"name": variable.Name})
	}
	payload := map[string]any{"variables": names}
	maps.Copy(payload, extra)
	writeJSON(response, payload)
}

func writeEnvironmentError(response http.ResponseWriter, err error) {
	var invalid *environment.InvalidError
	if errors.As(err, &invalid) {
		writeJSONStatus(response, http.StatusUnprocessableEntity, map[string]string{"error": invalid.Message})
		return
	}
	logInternalError("read or save environment variables", err)
	writeJSONStatus(response, http.StatusInternalServerError, map[string]string{"error": "Unable to read or save environment variables"})
}
