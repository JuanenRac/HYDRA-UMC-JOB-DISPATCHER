package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
)

// operationRequest is the common Operation of the SDK (operation.schema.json) as POST /jobs/submit reads it: the semantic planner hands
// a plan over in this shape. Only the fields that decide the job are read; the rest of the contract travels with the caller.
type operationRequest struct {
	SchemaVersion string         `json:"schema_version"`
	OperationID   string         `json:"operation_id"`
	Status        string         `json:"status"`
	Params        map[string]any `json:"params"`
}

// submitFromBody reads the body of POST /jobs/submit: a job, or - when it carries an operation_id - an Operation. The id of the
// operation is the id of the job and also its dedup key, so posting the same operation again is the same job; the priority,
// required_tool, location and depends_on of its params are the ones of the job. The operation has to be in the first state of the
// contract ("received"): nothing is authorized, queued or sent yet.
func submitFromBody(body []byte) (submitRequest, error) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(body, &probe); err != nil {
		return submitRequest{}, err
	}
	if _, isOperation := probe["operation_id"]; !isOperation {
		var req submitRequest
		err := json.Unmarshal(body, &req)
		return req, err
	}
	var op operationRequest
	if err := json.Unmarshal(body, &op); err != nil {
		return submitRequest{}, fmt.Errorf("operation: %w", err)
	}
	if op.SchemaVersion != "1.0" {
		return submitRequest{}, errors.New("operation: schema_version must be \"1.0\"")
	}
	if op.Status != "received" {
		return submitRequest{}, errors.New("operation: only an operation in the state \"received\" can become a job")
	}
	if op.OperationID == "" {
		return submitRequest{}, errors.New("operation: operation_id must not be empty")
	}
	if op.Params == nil {
		return submitRequest{}, errors.New("operation: params must be an object")
	}
	req := submitRequest{ID: op.OperationID, DedupKey: op.OperationID}
	if value, ok := op.Params["priority"]; ok {
		number, isNumber := value.(float64)
		if !isNumber || number != math.Trunc(number) || math.Abs(number) > math.MaxInt32 {
			return submitRequest{}, errors.New("operation: params.priority must be an integer")
		}
		req.Priority = int(number)
	}
	for name, target := range map[string]*string{"required_tool": &req.RequiredTool, "location": &req.Location} {
		if value, ok := op.Params[name]; ok {
			text, isText := value.(string)
			if !isText {
				return submitRequest{}, fmt.Errorf("operation: params.%s must be a string", name)
			}
			*target = text
		}
	}
	if value, ok := op.Params["depends_on"]; ok {
		items, isList := value.([]any)
		if !isList {
			return submitRequest{}, errors.New("operation: params.depends_on must be a list of strings")
		}
		for _, item := range items {
			text, isText := item.(string)
			if !isText {
				return submitRequest{}, errors.New("operation: params.depends_on must be a list of strings")
			}
			req.DependsOn = append(req.DependsOn, text)
		}
	}
	return req, nil
}
