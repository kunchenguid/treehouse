package config

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/BurntSushi/toml"
)

// decodeConfig is the common entry point for repository reads, approval hashing,
// and user-config reads/updates. Parse once, validate raw keys before the struct
// decoder can merge aliases, then decode Config from the same document.
// Successful results already use canonical
// schema names, so no alias can be preserved as an unknown setting on write.
func decodeConfig(data []byte) (Config, map[string]any, toml.MetaData, error) {
	cfg := DefaultConfig()
	var document toml.Primitive
	md, err := toml.Decode(string(data), &document)
	if err != nil {
		return cfg, nil, md, err
	}
	raw := make(map[string]any)
	if err := md.PrimitiveDecode(document, &raw); err != nil {
		return cfg, nil, md, err
	}
	if err := validateSchemaKeys(raw, reflect.TypeFor[Config](), ""); err != nil {
		return cfg, nil, md, err
	}
	if err := md.PrimitiveDecode(document, &cfg); err != nil {
		return cfg, nil, md, err
	}
	return cfg, raw, md, nil
}

// Struct fields are schema names; map keys (repository identities, for example)
// are data and must retain their exact spelling. Unknown settings remain opaque.
func validateSchemaKeys(raw map[string]any, schema reflect.Type, parent string) error {
	for _, field := range reflect.VisibleFields(schema) {
		if !field.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(field.Tag.Get("toml"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = field.Name
		}
		fieldPath := name
		if parent != "" {
			fieldPath = parent + "." + name
		}
		typ := field.Type
		for typ.Kind() == reflect.Pointer {
			typ = typ.Elem()
		}
		hint := fieldPath
		if typ.Kind() == reflect.Struct || typ.Kind() == reflect.Map {
			hint = "[" + fieldPath + "]"
		}
		for key := range raw {
			// This is also the struct decoder's comparison, including Unicode folds.
			// Reject every alias; TOML itself rejects exact duplicate keys.
			if key != name && strings.EqualFold(key, name) {
				return fmt.Errorf("noncanonical configuration key %q; use %s", key, hint)
			}
		}
		if typ.Kind() == reflect.Struct {
			if table, ok := raw[name].(map[string]any); ok {
				if err := validateSchemaKeys(table, typ, fieldPath); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
