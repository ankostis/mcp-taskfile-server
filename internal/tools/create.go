package tools

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"

	"github.com/go-task/task/v3/taskfile/ast"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// CreateToolForTask creates an MCP tool definition for a given task.
// The tool name is sanitized for MCP compatibility; the description
// references the original Taskfile task name for clarity. The prefix
// parameter is used in multi-root mode to namespace tool names. The
// taskfile is taken by value rather than via *Root so the planner can
// run on a snapshot without touching live, mutable server state.
func CreateToolForTask(tf *ast.Taskfile, prefix, taskName string, taskDef *ast.Task, logger *slog.Logger) *RegisteredTool {
	toolName := SanitizeToolName(prefixedToolName(prefix, taskName))

	logger.Debug("creating tool for task",
		slog.String("event", "tool.create"),
		slog.String("task_name", taskName),
		slog.String("tool_name", toolName),
		slog.String("prefix", prefix),
	)

	description := taskDef.Desc
	if description == "" {
		description = "Execute task: " + taskName
		logger.Debug("task has no description, using default",
			slog.String("event", "tool.desc_fallback"),
			slog.String("task_name", taskName),
		)
	}
	if toolName != taskName {
		description += fmt.Sprintf(" (task: %s)", taskName)
		logger.Debug("tool name differs from task name, appending task reference to description",
			slog.String("event", "tool.name_sanitized"),
			slog.String("task_name", taskName),
			slog.String("tool_name", toolName),
		)
	}

	// Build JSON Schema properties.
	//
	// Only enumerate global Taskfile vars: task-level `vars:` are applied
	// after caller-supplied values inside go-task's compiler, so advertising
	// them as MCP arguments would be a tool-contract lie. Required caller
	// inputs come exclusively from `requires:` below.
	properties := make(map[string]any)
	required := []string{}

	if tf.Vars != nil {
		for varName, varDef := range tf.Vars.All() {
			prop := map[string]any{
				"type": "string",
			}
			if strVal, ok := varDef.Value.(string); ok {
				prop["default"] = strVal
				prop["description"] = fmt.Sprintf("Variable: %s (default: %s)", varName, strVal)
				logger.Debug("adding global var with static default",
					slog.String("event", "tool.var_added"),
					slog.String("task_name", taskName),
					slog.String("var_name", varName),
					slog.String("default", strVal),
				)
			} else {
				prop["description"] = "Variable: " + varName
				logger.Debug("adding global var without static default",
					slog.String("event", "tool.var_added"),
					slog.String("task_name", taskName),
					slog.String("var_name", varName),
				)
			}
			properties[varName] = prop
		}
	}

	// Honour the task's `requires:` block: each named var becomes a
	// required property, and a static `enum:` translates directly to
	// JSON Schema `enum`. The `enum: { ref: .OTHER }` form is skipped
	// for now since it cannot be resolved without runtime context.
	if taskDef.Requires != nil {
		for _, req := range taskDef.Requires.Vars {
			if req == nil || req.Name == "" {
				continue
			}
			prop, ok := properties[req.Name].(map[string]any)
			if !ok {
				prop = map[string]any{
					"type":        "string",
					"description": "Required variable: " + req.Name,
				}
			}
			if req.Enum != nil && len(req.Enum.Value) > 0 {
				prop["enum"] = slices.Clone(req.Enum.Value)
				logger.Debug("adding required var with enum constraint",
					slog.String("event", "tool.required_var"),
					slog.String("task_name", taskName),
					slog.String("var_name", req.Name),
					slog.Any("enum", req.Enum.Value),
				)
			} else {
				logger.Debug("adding required var",
					slog.String("event", "tool.required_var"),
					slog.String("task_name", taskName),
					slog.String("var_name", req.Name),
				)
			}
			properties[req.Name] = prop
			required = append(required, req.Name)
		}
	}

	// Add MATCH parameter for wildcard tasks
	if isWildcardTask(taskName) {
		n := countWildcards(taskName)
		matchDesc := fmt.Sprintf("Wildcard values for task pattern %s (%d value(s) required, one per '*' segment)", taskName, n)
		properties["MATCH"] = map[string]any{
			"type":        "array",
			"items":       map[string]any{"type": "string"},
			"minItems":    n,
			"maxItems":    n,
			"description": matchDesc,
		}
		required = append(required, "MATCH")
		logger.Debug("wildcard task: added MATCH parameter",
			slog.String("event", "tool.wildcard"),
			slog.String("task_name", taskName),
			slog.Int("wildcard_count", n),
		)
	}

	schemaMap := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		schemaMap["required"] = required
	}

	schema, err := json.Marshal(schemaMap)
	if err != nil {
		schema = []byte(`{"type":"object"}`)
	}

	logger.Debug("tool created",
		slog.String("event", "tool.created"),
		slog.String("task_name", taskName),
		slog.String("tool_name", toolName),
		slog.Int("property_count", len(properties)),
		slog.Int("required_count", len(required)),
	)

	return &RegisteredTool{
		Tool: mcp.Tool{
			Name:        toolName,
			Description: description,
			InputSchema: json.RawMessage(schema),
		},
		schemaBytes: schema,
	}
}
