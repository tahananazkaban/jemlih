// Package events defines the event envelope, validates payloads against the schemas and builds outbox items.
package events

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// EnvelopeSchema is the schema file every published event must match.
const EnvelopeSchema = "envelope.v1.json"

//go:embed schemas/*.json
var schemaFiles embed.FS

// Envelope wraps every event published to the bus.
type Envelope struct {
	EventID    string          `json:"event_id"`
	Type       string          `json:"type"`
	Version    int             `json:"version"`
	OccurredAt time.Time       `json:"occurred_at"`
	RentalID   string          `json:"rental_id"`
	Source     string          `json:"source"`
	Data       json.RawMessage `json:"data"`
}

// New builds a validated envelope for an event that happened at now.
func New(source, rentalID, eventType string, version int, data any, now time.Time) (Envelope, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return Envelope{}, fmt.Errorf("marshal %s data: %w", eventType, err)
	}
	if err := Validate(eventType, version, raw); err != nil {
		return Envelope{}, err
	}
	e := Envelope{
		EventID:    uuid.Must(uuid.NewV7()).String(),
		Type:       eventType,
		Version:    version,
		OccurredAt: now.UTC(),
		RentalID:   rentalID,
		Source:     source,
		Data:       raw,
	}
	whole, err := json.Marshal(e)
	if err != nil {
		return Envelope{}, fmt.Errorf("marshal %s envelope: %w", eventType, err)
	}
	if err := ValidateAgainst(EnvelopeSchema, whole); err != nil {
		return Envelope{}, err
	}
	return e, nil
}

// SchemaName is the schema file of an event type and version, e.g. UnitCreated.v1.json.
func SchemaName(eventType string, version int) string {
	return fmt.Sprintf("%s.v%d.json", eventType, version)
}

// Validate checks data against the schema of eventType at version.
func Validate(eventType string, version int, data []byte) error {
	return ValidateAgainst(SchemaName(eventType, version), data)
}

// ValidateAgainst checks a JSON document against one schema file.
func ValidateAgainst(schemaName string, doc []byte) error {
	schema, err := compiled(schemaName)
	if err != nil {
		return err
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(doc))
	if err != nil {
		return fmt.Errorf("%s: invalid JSON: %w", schemaName, err)
	}
	if err := schema.Validate(inst); err != nil {
		return fmt.Errorf("%s: %w", schemaName, err)
	}
	return nil
}

var (
	compileOnce sync.Once
	compiler    *jsonschema.Compiler
	compileErr  error
	cacheMu     sync.Mutex
	cache       = map[string]*jsonschema.Schema{}
)

func compiled(name string) (*jsonschema.Schema, error) {
	compileOnce.Do(func() { compiler, compileErr = loadSchemas() })
	if compileErr != nil {
		return nil, compileErr
	}
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if s, ok := cache[name]; ok {
		return s, nil
	}
	s, err := compiler.Compile(name)
	if err != nil {
		return nil, fmt.Errorf("unknown event schema %s: %w", name, err)
	}
	cache[name] = s
	return s, nil
}

func loadSchemas() (*jsonschema.Compiler, error) {
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	entries, err := fs.ReadDir(schemaFiles, "schemas")
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		raw, err := schemaFiles.ReadFile("schemas/" + e.Name())
		if err != nil {
			return nil, err
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("schema %s: %w", e.Name(), err)
		}
		if err := c.AddResource(e.Name(), doc); err != nil {
			return nil, fmt.Errorf("schema %s: %w", e.Name(), err)
		}
	}
	return c, nil
}
