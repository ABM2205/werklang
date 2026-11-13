#include "tree_sitter/parser.h"
#include <stdbool.h>
#include <stdint.h>

enum TokenType {
  CONFIG_KEYWORD,
  PKL_BODY,
};

void *tree_sitter_werklang_external_scanner_create(void) { return NULL; }
void tree_sitter_werklang_external_scanner_destroy(void *payload) { (void)payload; }
unsigned tree_sitter_werklang_external_scanner_serialize(void *payload, char *buffer) {
  (void)payload;
  (void)buffer;
  return 0;
}
void tree_sitter_werklang_external_scanner_deserialize(void *payload, const char *buffer, unsigned length) {
  (void)payload;
  (void)buffer;
  (void)length;
}

static void advance(TSLexer *lexer) { lexer->advance(lexer, false); }
static void skip(TSLexer *lexer) { lexer->advance(lexer, true); }

static bool is_ident_start(int32_t c) {
  return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c >= 0x80;
}

static bool is_ident_part(int32_t c) {
  return is_ident_start(c) || (c >= '0' && c <= '9');
}

static void skip_go_trivia(TSLexer *lexer) {
  while (!lexer->eof(lexer)) {
    int32_t c = lexer->lookahead;
    if (c == ' ' || c == '\t' || c == '\r' || c == '\n') {
      advance(lexer);
      continue;
    }
    if (c == '/') {
      advance(lexer);
      if (lexer->lookahead == '/') {
        while (!lexer->eof(lexer) && lexer->lookahead != '\n') {
          advance(lexer);
        }
        continue;
      }
      if (lexer->lookahead == '*') {
        advance(lexer);
        while (!lexer->eof(lexer)) {
          if (lexer->lookahead == '*') {
            advance(lexer);
            if (lexer->lookahead == '/') {
              advance(lexer);
              break;
            }
            continue;
          }
          advance(lexer);
        }
        continue;
      }
      return;
    }
    break;
  }
}

static bool try_scan_config_keyword(TSLexer *lexer) {
  while (lexer->lookahead == ' ' || lexer->lookahead == '\t' || lexer->lookahead == '\r' || lexer->lookahead == '\n') {
    skip(lexer);
  }
  const char *kw = "config";
  for (int i = 0; i < 6; i++) {
    if (lexer->lookahead != kw[i]) {
      return false;
    }
    advance(lexer);
  }
  if (is_ident_part(lexer->lookahead)) {
    return false;
  }

  // Mark end right after `config`, then look ahead for `[ident] {`!
  lexer->mark_end(lexer);
  skip_go_trivia(lexer);

  if (is_ident_start(lexer->lookahead)) {
    while (!lexer->eof(lexer) && is_ident_part(lexer->lookahead)) {
      advance(lexer);
    }
    skip_go_trivia(lexer);
  }

  if (lexer->lookahead == '{') {
    lexer->result_symbol = CONFIG_KEYWORD;
    return true;
  }
  return false;
}

static void scan_pkl_string(TSLexer *lexer, unsigned hashes);

static void scan_pkl_interpolation(TSLexer *lexer) {
  int paren_depth = 1;
  while (!lexer->eof(lexer) && paren_depth > 0) {
    int32_t c = lexer->lookahead;
    if (c == '"') {
      scan_pkl_string(lexer, 0);
      continue;
    }
    if (c == '#') {
      unsigned hashes = 0;
      while (lexer->lookahead == '#') {
        hashes++;
        advance(lexer);
      }
      if (lexer->lookahead == '"') {
        scan_pkl_string(lexer, hashes);
      }
      continue;
    }
    if (c == '(') {
      paren_depth++;
      advance(lexer);
      continue;
    }
    if (c == ')') {
      paren_depth--;
      advance(lexer);
      continue;
    }
    advance(lexer);
  }
}

static bool match_hashes(TSLexer *lexer, unsigned hashes) {
  for (unsigned i = 0; i < hashes; i++) {
    if (lexer->lookahead != '#') {
      return false;
    }
    advance(lexer);
  }
  return true;
}

static void scan_pkl_string(TSLexer *lexer, unsigned hashes) {
  advance(lexer);
  bool multiline = false;
  if (lexer->lookahead == '"') {
    advance(lexer);
    if (lexer->lookahead == '"') {
      multiline = true;
      advance(lexer);
    } else if (hashes == 0) {
      return;
    }
  }

  while (!lexer->eof(lexer)) {
    int32_t c = lexer->lookahead;
    if (!multiline && hashes == 0 && c == '\n') {
      return;
    }
    if (c == '"') {
      advance(lexer);
      if (multiline) {
        if (lexer->lookahead == '"') {
          advance(lexer);
          if (lexer->lookahead == '"') {
            advance(lexer);
            if (match_hashes(lexer, hashes)) {
              return;
            }
          }
        }
      } else {
        if (match_hashes(lexer, hashes)) {
          return;
        }
      }
      continue;
    }
    if (c == '\\') {
      advance(lexer);
      if (match_hashes(lexer, hashes)) {
        if (lexer->lookahead == '(') {
          advance(lexer);
          scan_pkl_interpolation(lexer);
        } else if (!lexer->eof(lexer)) {
          advance(lexer);
        }
      }
      continue;
    }
    advance(lexer);
  }
}

bool tree_sitter_werklang_external_scanner_scan(
    void *payload,
    TSLexer *lexer,
    const bool *valid_symbols
) {
  (void)payload;

  if (valid_symbols[PKL_BODY]) {
    if (lexer->lookahead == '}' || lexer->eof(lexer)) {
      return false;
    }

    int brace_depth = 1;
    bool consumed_any = false;

    while (!lexer->eof(lexer)) {
      int32_t c = lexer->lookahead;

      if (c == '}') {
        if (brace_depth == 1) {
          lexer->mark_end(lexer);
          lexer->result_symbol = PKL_BODY;
          return consumed_any;
        }
        brace_depth--;
        advance(lexer);
        consumed_any = true;
        continue;
      }

      if (c == '{') {
        brace_depth++;
        advance(lexer);
        consumed_any = true;
        continue;
      }

      if (c == '/') {
        advance(lexer);
        consumed_any = true;
        if (lexer->lookahead == '/') {
          while (!lexer->eof(lexer) && lexer->lookahead != '\n') {
            advance(lexer);
          }
          continue;
        }
        if (lexer->lookahead == '*') {
          advance(lexer);
          int comment_depth = 1;
          while (!lexer->eof(lexer) && comment_depth > 0) {
            if (lexer->lookahead == '/') {
              advance(lexer);
              if (lexer->lookahead == '*') {
                advance(lexer);
                comment_depth++;
              }
              continue;
            }
            if (lexer->lookahead == '*') {
              advance(lexer);
              if (lexer->lookahead == '/') {
                advance(lexer);
                comment_depth--;
              }
              continue;
            }
            advance(lexer);
          }
          continue;
        }
        continue;
      }

      if (c == '#') {
        unsigned hashes = 0;
        while (lexer->lookahead == '#') {
          hashes++;
          advance(lexer);
        }
        consumed_any = true;
        if (lexer->lookahead == '"') {
          scan_pkl_string(lexer, hashes);
        }
        continue;
      }

      if (c == '"') {
        consumed_any = true;
        scan_pkl_string(lexer, 0);
        continue;
      }

      if (c == '`') {
        advance(lexer);
        consumed_any = true;
        while (!lexer->eof(lexer) && lexer->lookahead != '`' && lexer->lookahead != '\n') {
          advance(lexer);
        }
        if (lexer->lookahead == '`') {
          advance(lexer);
        }
        continue;
      }

      advance(lexer);
      consumed_any = true;
    }
    return false;
  }

  if (valid_symbols[CONFIG_KEYWORD]) {
    return try_scan_config_keyword(lexer);
  }

  return false;
}
