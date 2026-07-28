// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
)

var configType = reflect.TypeOf(Config{})

const CurrentWorkspaceOverrideSchemaVersion = 1

// decodeConfigObject parses one full configuration object.
func decodeConfigObject(raw []byte, source string) (map[string]any, error) {
	return decodeConfigDocument(raw, source, false)
}

// decodeWorkspaceOverrideObject parses a partial workspace override and
// accepts the version metadata owned by that persistence format.
func decodeWorkspaceOverrideObject(raw []byte, source string) (map[string]any, error) {
	return decodeConfigDocument(raw, source, true)
}

// decodeConfigDocument rejects duplicate keys at every depth, removes
// explicitly supported legacy fields, and rejects unknown Config fields.
func decodeConfigDocument(raw []byte, source string, workspaceOverride bool) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := decodeConfigValue(decoder, source, "$")
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("parse config %s: trailing JSON value", source)
		}
		return nil, fmt.Errorf("parse config %s: %w", source, err)
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("parse config %s: top-level value must be an object", source)
	}
	if workspaceOverride {
		if err := consumeWorkspaceOverrideVersion(object, source); err != nil {
			return nil, err
		}
	}
	stripLegacyConfigFields(object)
	if err := validateConfigFields(object, configType, source, "$"); err != nil {
		return nil, err
	}
	return object, nil
}

func consumeWorkspaceOverrideVersion(object map[string]any, source string) error {
	raw, exists := object["schemaVersion"]
	if !exists {
		return nil
	}
	delete(object, "schemaVersion")
	number, ok := raw.(json.Number)
	if !ok {
		return fmt.Errorf("parse config %s: $.schemaVersion must be an integer", source)
	}
	version, err := number.Int64()
	if err != nil || version != CurrentWorkspaceOverrideSchemaVersion {
		return fmt.Errorf("parse config %s: unsupported workspace override schemaVersion %s", source, number.String())
	}
	return nil
}

func decodeConfigValue(decoder *json.Decoder, source, path string) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("parse config %s at %s: %w", source, path, err)
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return token, nil
	}
	switch delimiter {
	case '{':
		object := map[string]any{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, fmt.Errorf("parse config %s at %s: %w", source, path, err)
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, fmt.Errorf("parse config %s at %s: object key must be a string", source, path)
			}
			childPath := path + "." + key
			if _, exists := object[key]; exists {
				return nil, fmt.Errorf("parse config %s: duplicate key %s", source, childPath)
			}
			value, err := decodeConfigValue(decoder, source, childPath)
			if err != nil {
				return nil, err
			}
			object[key] = value
		}
		if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
			return nil, fmt.Errorf("parse config %s at %s: malformed object", source, path)
		}
		return object, nil
	case '[':
		values := make([]any, 0)
		for index := 0; decoder.More(); index++ {
			value, err := decodeConfigValue(decoder, source, fmt.Sprintf("%s[%d]", path, index))
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		if end, err := decoder.Token(); err != nil || end != json.Delim(']') {
			return nil, fmt.Errorf("parse config %s at %s: malformed array", source, path)
		}
		return values, nil
	default:
		return nil, fmt.Errorf("parse config %s at %s: unexpected delimiter %q", source, path, delimiter)
	}
}

func stripLegacyConfigFields(object map[string]any) {
	for _, key := range []string{"database", "figmaDesktopMcpUrl", "figmaDesktopTimeoutMs"} {
		delete(object, key)
	}
	servers, _ := object["mcpServers"].(map[string]any)
	for _, raw := range servers {
		server, _ := raw.(map[string]any)
		delete(server, "toolPrefix")
	}
}

func validateConfigFields(value any, target reflect.Type, source, path string) error {
	if value == nil {
		return nil
	}
	for target.Kind() == reflect.Pointer {
		target = target.Elem()
	}
	switch target.Kind() {
	case reflect.Interface:
		return nil
	case reflect.Struct:
		object, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		fields := jsonFieldTypes(target)
		for key, child := range object {
			fieldType, exists := fields[key]
			if !exists {
				return fmt.Errorf("parse config %s: unknown field %s.%s", source, path, key)
			}
			if err := validateConfigFields(child, fieldType, source, path+"."+key); err != nil {
				return err
			}
		}
	case reflect.Map:
		object, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		for key, child := range object {
			if err := validateConfigFields(child, target.Elem(), source, path+"."+key); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		values, ok := value.([]any)
		if !ok {
			return nil
		}
		for index, child := range values {
			if err := validateConfigFields(child, target.Elem(), source, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	}
	return nil
}

func jsonFieldTypes(target reflect.Type) map[string]reflect.Type {
	fields := make(map[string]reflect.Type, target.NumField())
	for index := 0; index < target.NumField(); index++ {
		field := target.Field(index)
		if !field.IsExported() {
			continue
		}
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name == "-" {
			continue
		}
		if name == "" {
			name = field.Name
		}
		fields[name] = field.Type
	}
	return fields
}
