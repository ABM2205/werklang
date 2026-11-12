use zed_extension_api::{self as zed, settings::LspSettings, LanguageServerId, Result};

struct WerklangExtension;

impl zed::Extension for WerklangExtension {
    fn new() -> Self {
        Self
    }

    fn language_server_command(
        &mut self,
        language_server_id: &LanguageServerId,
        worktree: &zed::Worktree,
    ) -> Result<zed::Command> {
        if language_server_id.as_ref() != "werklang-lsp" {
            return Err(format!("unknown language server: {language_server_id}"));
        }

        let binary_settings = LspSettings::for_worktree("werklang-lsp", worktree)
            .ok()
            .and_then(|settings| settings.binary);

        let command = match binary_settings.as_ref().and_then(|b| b.path.clone()) {
            Some(path) => path,
            None => worktree.which("werk").ok_or_else(|| {
                "werk binary not found on PATH; install werklang or configure lsp.werklang-lsp.binary.path".to_string()
            })?,
        };

        let args = binary_settings
            .as_ref()
            .and_then(|b| b.arguments.clone())
            .unwrap_or_else(|| vec!["lsp".to_string()]);

        let mut env = worktree.shell_env();
        if let Some(extra_env) = binary_settings.and_then(|b| b.env) {
            env.extend(extra_env);
        }

        Ok(zed::Command { command, args, env })
    }
}

zed::register_extension!(WerklangExtension);
