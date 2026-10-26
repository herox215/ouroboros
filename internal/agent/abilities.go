package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"ouroboros/internal/a2a"
	"ouroboros/internal/openai"
)

// Ability is a lazily exposed group of tools and their operating instructions.
// Tools remain subject to the normal allowlist, settings and permission checks.
// Instructions grant no privileges to other tools: dependencies must be allowed
// separately, or implemented inside the ability's own narrowly scoped tools.
type Ability struct {
	Name, Description, Instructions string
	Tools                           []Tool
}

var abilityName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// ExpandAbilities exposes definitions to configuration selection, not the LLM.
// It validates the entire namespace before selection, so a collision cannot
// silently replace an ordinary tool, even when that tool is withheld.
func ExpandAbilities(tools []Tool) ([]Tool, error) {
	out := slices.Clone(tools)
	if !slices.ContainsFunc(tools, func(t Tool) bool { return t.AbilityCatalog != nil }) {
		return out, nil // preserve ordinary tool selection and already-expanded catalogs
	}
	names := map[string]bool{}
	for _, t := range tools {
		if names[t.Name] {
			return nil, fmt.Errorf("duplicate tool %q", t.Name)
		}
		names[t.Name] = true
	}
	abilities := map[string]bool{}
	for i, t := range tools {
		if t.AbilityCatalog == nil {
			continue
		}
		catalog := t.AbilityCatalog()
		out[i].AbilityCatalog = nil
		out[i].catalog = slices.Clone(catalog)
		out[i].OwnerOnly = true
		out[i].Description += " Catalog:"
		for _, a := range catalog {
			if !abilityName.MatchString(a.Name) || abilities[a.Name] {
				return nil, fmt.Errorf("invalid or duplicate ability %q", a.Name)
			}
			abilities[a.Name] = true
			a.Tools = slices.Clone(a.Tools)
			out[i].Description += " " + a.Name + ": " + a.Description + ";"
			for _, tool := range a.Tools {
				if !abilityName.MatchString(tool.Name) || names[tool.Name] || tool.AbilityCatalog != nil || tool.Run == nil || !json.Valid([]byte(tool.Schema)) {
					return nil, fmt.Errorf("ability %s: invalid or colliding tool %q", a.Name, tool.Name)
				}
				names[tool.Name] = true
				tool.OwnerOnly = true
				tool.ability = &a
				if configure := tool.Configure; configure != nil {
					tool.Configure = func(raw json.RawMessage) (Tool, error) {
						configured, err := configure(raw)
						if err != nil {
							return Tool{}, err
						}
						if configured.Name != tool.Name {
							return Tool{}, fmt.Errorf("ability tool configuration changed name %q", tool.Name)
						}
						configured.ability, configured.OwnerOnly = tool.ability, true
						return configured, nil
					}
				}
				out = append(out, tool)
			}
		}
	}
	// Keep the catalog available when only the inspector was selected. This is
	// metadata, not a grant; activation still requires every configured tool.
	var catalog []Ability
	for _, t := range out {
		catalog = append(catalog, t.catalog...)
	}
	for i := range out {
		if out[i].ability == nil {
			out[i].catalog = catalog
		}
	}
	return out, nil
}

type abilityRuntimeKey struct{}
type abilityRuntime struct {
	agent   *Agent
	id      string
	catalog []Ability
	hidden  map[string]Tool
	pending []string
	loaded  []string
	system  string
	fresh   map[string]bool // tools exposed at this model-call boundary
}

func abilityRuntimeOf(ctx context.Context) (*abilityRuntime, error) {
	if _, peer := a2a.OriginFrom(ctx); peer {
		return nil, fmt.Errorf("abilities are for the owner only")
	}
	r, _ := ctx.Value(abilityRuntimeKey{}).(*abilityRuntime)
	if r == nil {
		return nil, fmt.Errorf("abilities require an active owner conversation")
	}
	return r, nil
}

// InspectAbility returns instructions and tool names without granting access.
func InspectAbility(ctx context.Context, name string) (string, error) {
	r, err := abilityRuntimeOf(ctx)
	if err != nil {
		return "", err
	}
	a, err := r.find(name)
	if err != nil {
		return "", err
	}
	var names []string
	for _, t := range a.Tools {
		status := "allowed"
		if _, ok := r.hidden[t.Name]; !ok {
			status = "withheld"
		}
		names = append(names, t.Name+" ("+status+")")
	}
	return a.Name + ": " + a.Description + "\n" + a.Instructions + "\nTools: " + strings.Join(names, ", "), nil
}

// LoadAbility grants the configured tools starting at the next model call.
// A partial allowlist is refused atomically; inspection explains what is missing.
func LoadAbility(ctx context.Context, name string) (string, error) {
	r, err := abilityRuntimeOf(ctx)
	if err != nil {
		return "", err
	}
	a, err := r.find(name)
	if err != nil {
		return "", err
	}
	for _, t := range a.Tools {
		if _, ok := r.hidden[t.Name]; !ok {
			return "", fmt.Errorf("ability %s: tool %s is not allowed; loading does not override the tool allowlist", name, t.Name)
		}
	}
	if !slices.Contains(r.loaded, name) && !slices.Contains(r.pending, name) {
		r.pending = append(r.pending, name)
		r.agent.memory.mu.Lock()
		c := r.agent.memory.conversations[r.id]
		if c == nil {
			c = &conversation{}
			r.agent.memory.conversations[r.id] = c
		}
		if !slices.Contains(c.abilities, name) {
			c.abilities = append(c.abilities, name)
		}
		r.agent.memory.mu.Unlock()
	}
	return "Loaded " + name + " for this conversation; tools become available on the next model call.\n" + a.Instructions, nil
}

func (r *abilityRuntime) find(name string) (Ability, error) {
	for _, a := range r.catalog {
		if a.Name == name {
			return a, nil
		}
	}
	return Ability{}, fmt.Errorf("unknown ability %q", name)
}

// activate runs only at model-call boundaries. Calls in the loader's own
// response batch cannot use newly loaded tools, even if the model guesses names.
func (r *abilityRuntime) activate() {
	r.fresh = map[string]bool{}
	for _, name := range r.pending {
		a, err := r.find(name)
		if err != nil {
			continue
		}
		allowed := true
		for _, t := range a.Tools {
			if _, ok := r.hidden[t.Name]; !ok {
				allowed = false
			}
		}
		if !allowed {
			continue
		}
		for _, t := range a.Tools {
			tool := r.hidden[t.Name]
			r.agent.tools[t.Name] = tool
			r.fresh[t.Name] = true
			r.agent.specs = append(r.agent.specs, openai.NewTool(tool.Name, tool.Description, tool.Schema))
		}
		r.loaded = append(r.loaded, name)
	}
	r.pending = nil
	r.agent.system.Content = r.system
	for _, name := range r.loaded {
		if a, err := r.find(name); err == nil {
			r.agent.system.Content += "\n\nAbility " + name + ":\n" + a.Instructions
		}
	}
	b, _ := json.Marshal(r.agent.specs)
	r.agent.sizeOf = len(b)
}

func (a *Agent) abilityAnswer(ctx context.Context, id string) (*Agent, context.Context, *abilityRuntime) {
	copy := *a
	copy.tools = map[string]Tool{}
	copy.specs = slices.Clone(a.specs)
	r := &abilityRuntime{agent: &copy, id: id, hidden: map[string]Tool{}, system: a.system.Content}
	for name, t := range a.tools {
		if t.ability != nil {
			r.hidden[name] = t
			continue
		}
		copy.tools[name] = t
		if len(r.catalog) == 0 {
			r.catalog = t.catalog
		}
	}
	if _, peer := a2a.OriginFrom(ctx); !peer {
		a.memory.mu.Lock()
		if c := a.memory.conversations[id]; c != nil {
			r.pending = slices.Clone(c.abilities)
		}
		a.memory.mu.Unlock()
	}
	return &copy, context.WithValue(ctx, abilityRuntimeKey{}, r), r
}
