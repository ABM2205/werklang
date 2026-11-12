package pklinterop

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ABM2205/werklang/internal/diag"
	"github.com/ABM2205/werklang/internal/types"
	"github.com/apple/pkl-go/pkl"
)

// FindPklBinary locates the pkl CLI executable, including ~/.local/bin/pkl when
// launched from GUI editors like Zed whose PATH may be minimal.
func FindPklBinary() (string, error) {
	if p, err := exec.LookPath("pkl"); err == nil {
		return p, nil
	}
	if home, err := os.UserHomeDir(); err == nil {
		cand := filepath.Join(home, ".local", "bin", "pkl")
		if st, err := os.Stat(cand); err == nil && !st.IsDir() {
			return cand, nil
		}
	}
	for _, cand := range []string{"/usr/local/bin/pkl", "/usr/bin/pkl"} {
		if st, err := os.Stat(cand); err == nil && !st.IsDir() {
			return cand, nil
		}
	}
	return "", fmt.Errorf("pkl CLI binary not found on PATH or ~/.local/bin/pkl")
}

func isURI(spec string) bool {
	return strings.HasPrefix(spec, "package://") ||
		strings.HasPrefix(spec, "https://") ||
		strings.HasPrefix(spec, "http://") ||
		strings.HasPrefix(spec, "file://")
}

// InspectModule evaluates a .pkl module (file or URI) via pkl-go and constructs
// a typed Werklang ModuleType schema (plus any nested object struct types).
func InspectModule(baseDir, importSpec, alias, goTypePrefix string) (*types.ModuleType, string, error) {
	pklBin, err := FindPklBinary()
	if err != nil {
		return nil, "", err
	}

	ctx := context.Background()
	ev, err := pkl.NewEvaluatorWithCommand(ctx, []string{pklBin}, pkl.PreconfiguredOptions)
	if err != nil {
		return nil, "", fmt.Errorf("starting pkl evaluator: %w", err)
	}
	defer ev.Close()

	var src *pkl.ModuleSource
	resolvedPath := importSpec
	var localLines []string
	if isURI(importSpec) {
		src = pkl.UriSource(importSpec)
	} else {
		if !filepath.IsAbs(resolvedPath) {
			resolvedPath = filepath.Join(baseDir, resolvedPath)
		}
		abs, err := filepath.Abs(resolvedPath)
		if err == nil {
			resolvedPath = abs
		}
		if raw, err := os.ReadFile(resolvedPath); err == nil {
			localLines = strings.Split(string(raw), "\n")
		}
		src = pkl.FileSource(resolvedPath)
	}

	var obj pkl.Object
	if err := ev.EvaluateModule(ctx, src, &obj); err != nil {
		return nil, "", fmt.Errorf("evaluating pkl module %q: %w", importSpec, err)
	}

	mod := buildModuleFromObject(alias, goTypePrefix, resolvedPath, localLines, &obj)
	return mod, resolvedPath, nil
}

func buildModuleFromObject(name, goTypeName, sourcePath string, lines []string, obj *pkl.Object) *types.ModuleType {
	mod := types.NewModuleType(name, goTypeName, types.ModPkl, sourcePath)

	keys := make([]string, 0, len(obj.Properties))
	for k := range obj.Properties {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		val := obj.Properties[k]
		ft, nested := inferPklValueType(k, goTypeName+"_"+types.ExportGoIdent(k), sourcePath, lines, val)
		if ft == nil {
			continue
		}
		mod.Nested = append(mod.Nested, nested...)
		span := findPropertySpan(sourcePath, lines, k)
		mod.AddField(&types.ModuleField{
			Name:        k,
			GoFieldName: types.ExportGoIdent(k),
			Type:        ft,
			Required:    false,
			DefSpan:     span,
			Doc:         fmt.Sprintf("Pkl property %s (%s)", k, ft.String()),
		})
	}
	return mod
}

func inferPklValueType(propName, nestedGoName, sourcePath string, lines []string, val any) (types.Type, []*types.ModuleType) {
	switch v := val.(type) {
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return types.Int, nil
	case float32, float64:
		return types.Float, nil
	case string:
		return types.String, nil
	case bool:
		return types.Bool, nil
	case pkl.Duration, *pkl.Duration:
		return types.Int, nil
	case pkl.DataSize, *pkl.DataSize:
		return types.Int, nil
	case []any:
		elemType, nested := inferSliceElemType(propName, nestedGoName, sourcePath, lines, v)
		return &types.SliceType{Elem: elemType}, nested
	case pkl.Object:
		return inferObjectType(propName, nestedGoName, sourcePath, lines, &v)
	case *pkl.Object:
		if v == nil {
			return types.Invalid, nil
		}
		return inferObjectType(propName, nestedGoName, sourcePath, lines, v)
	default:
		// Unsupported Pkl types (e.g. Map or nil) mark as Invalid so accessing them reports a clear error
		return types.Invalid, nil
	}
}

func inferObjectType(propName, nestedGoName, sourcePath string, lines []string, obj *pkl.Object) (types.Type, []*types.ModuleType) {
	// Pkl Listing / Dynamic list-like objects expose Elements without Properties
	if len(obj.Properties) == 0 && len(obj.Elements) > 0 {
		elemType, nested := inferSliceElemType(propName, nestedGoName, sourcePath, lines, obj.Elements)
		return &types.SliceType{Elem: elemType}, nested
	}
	if len(obj.Entries) > 0 && len(obj.Properties) == 0 {
		// Pkl Mapping / Dynamic key-value map (outside Werklang's type subset)
		return types.Invalid, nil
	}
	nestedMod := buildModuleFromObject(propName, nestedGoName, sourcePath, lines, obj)
	allNested := append([]*types.ModuleType{}, nestedMod.Nested...)
	nestedMod.Nested = nil
	allNested = append(allNested, nestedMod)
	return nestedMod, allNested
}

func inferSliceElemType(propName, nestedGoName, sourcePath string, lines []string, elems []any) (types.Type, []*types.ModuleType) {
	if len(elems) == 0 {
		return types.String, nil
	}
	return inferPklValueType(propName+"Item", nestedGoName+"Item", sourcePath, lines, elems[0])
}

func findPropertySpan(sourcePath string, lines []string, prop string) diag.Span {
	for lineIdx, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, prop) {
			rest := strings.TrimSpace(trimmed[len(prop):])
			if strings.HasPrefix(rest, "=") || strings.HasPrefix(rest, ":") || strings.HasPrefix(rest, "{") {
				col := len(line) - len(trimmed)
				return diag.Span{
					File:  sourcePath,
					Start: diag.Pos{Line: lineIdx, Col: col},
					End:   diag.Pos{Line: lineIdx, Col: col + len(prop)},
				}
			}
		}
	}
	return diag.Span{File: sourcePath}
}
