package pklschema

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const defaultPklProject = `amends "pkl:Project"

evaluatorSettings {
  allowedModules {
    "pkl:"
    "repl:"
    "file:"
    "https:"
    "package:"
    "projectpackage:"
    "werk:"
  }
  externalModuleReaders {
    ["werk"] {
      executable = "werk"
      arguments {
        "pkl-reader"
      }
    }
  }
}
`

// InitProject scaffolds or updates a PklProject file in dir so standalone `pkl`
// CLI commands and `pkl-lsp` resolve `amends "werk:/..."` automatically.
func InitProject(dir string) (string, bool, error) {
	if dir == "" {
		dir = "."
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", false, err
	}
	pklProjPath := filepath.Join(absDir, "PklProject")
	existing, err := os.ReadFile(pklProjPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", false, err
		}
		if err := os.WriteFile(pklProjPath, []byte(defaultPklProject), 0o644); err != nil {
			return "", false, err
		}
		return pklProjPath, true, nil
	}

	content := string(existing)
	if strings.Contains(content, `["werk"]`) && strings.Contains(content, `"werk:"`) {
		return pklProjPath, false, nil
	}

	if strings.Contains(content, "evaluatorSettings") {
		return pklProjPath, false, fmt.Errorf(
			"%s already defines evaluatorSettings without the 'werk' reader; add \"werk:\" to allowedModules and [\"werk\"] { executable = \"werk\"; arguments { \"pkl-reader\" } } to externalModuleReaders",
			pklProjPath,
		)
	}

	updated := strings.TrimRight(content, "\n") + "\n\n" + strings.TrimPrefix(defaultPklProject, "amends \"pkl:Project\"\n\n")
	if err := os.WriteFile(pklProjPath, []byte(updated), 0o644); err != nil {
		return "", false, err
	}
	return pklProjPath, true, nil
}
