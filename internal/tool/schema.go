package tool

import (
	"encoding/json"

	"github.com/invopop/jsonschema"
)

func GenerateParametersSchema(args any) map[string]interface{} {
	reflector := jsonschema.Reflector{
		DoNotReference: true,
	}

	schema := reflector.Reflect(args)

	data, err := json.Marshal(schema)
	if err != nil {
		return map[string]interface{}{}
	}

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return map[string]interface{}{}
	}

	return result
}