// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
)

func completionCmd(out io.Writer, args []string) error {
	fs := flag.NewFlagSet("completion", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), `chameleon completion <bash|zsh|fish> — print shell completion script.

Source the output in your shell init file (or eval it directly) to get
subcommand completion plus the canonical flags for each subcommand.

Usage:
    chameleon completion bash >> ~/.bashrc       # bash
    chameleon completion zsh > ~/.zsh-cham       # zsh; source from .zshrc
    chameleon completion fish > ~/.config/fish/completions/chameleon.fish

The completion data is static (compiled in at build time); regenerate
after upgrading chameleon to pick up new subcommands or flags.`)
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("exactly one shell argument required (bash|zsh|fish)")
	}
	switch fs.Arg(0) {
	case "bash":
		fmt.Fprint(out, completionBash)
	case "zsh":
		fmt.Fprint(out, completionZsh)
	case "fish":
		fmt.Fprint(out, completionFish)
	default:
		fs.Usage()
		return fmt.Errorf("unsupported shell %q (want bash, zsh, or fish)", fs.Arg(0))
	}
	return nil
}

const completionBash = `# chameleon bash completion
_chameleon() {
    local cur prev words cword
    _init_completion || return

    local subcmds="explain verify check-schema migrate hash lint version completion help"

    if [[ $cword -eq 1 ]]; then
        COMPREPLY=( $(compgen -W "$subcmds" -- "$cur") )
        return
    fi

    case "${words[1]}" in
        explain)
            COMPREPLY=( $(compgen -W "--schema --message --chain --layers --env --field --ctx --help" -- "$cur") )
            ;;
        verify)
            COMPREPLY=( $(compgen -W "--chain --layers --env --ctx --strict --help" -- "$cur") )
            ;;
        check-schema)
            COMPREPLY=( $(compgen -W "--schema --message --include --exclude --help" -- "$cur") )
            ;;
        migrate)
            COMPREPLY=( $(compgen -W "--chain --layers --env --ctx --transform --path --from --to --value --int --bool --only --dry-run --help" -- "$cur") )
            ;;
        hash)
            _filedir
            ;;
        lint)
            # singlechecker driver — accepts package patterns + standard
            # analysis flags. Completion just hands off to filedir for
            # ./... style invocations.
            _filedir
            ;;
        completion)
            COMPREPLY=( $(compgen -W "bash zsh fish" -- "$cur") )
            ;;
    esac
}
complete -F _chameleon chameleon
`

const completionZsh = `#compdef chameleon
_chameleon() {
    local -a subcmds
    subcmds=(
        'explain:Resolve a config and print per-field provenance'
        'verify:Check @header.content_sha256 of every layer in a chain'
        'check-schema:Check .proto files and their imports against the PXF bind-time rules'
        'migrate:Bulk-edit layer files in a chain spec'
        'hash:Print the canonical body SHA-256 of a single layer file'
        'lint:Flag pxfv1.Secret.Value reads / writes in consumer code'
        'version:Print chameleon version'
        'completion:Print shell completion script'
        'help:Show top-level help'
    )

    if (( CURRENT == 2 )); then
        _describe 'subcommand' subcmds
        return
    fi

    case "${words[2]}" in
        explain)
            _arguments \
                '--schema=[.proto file describing the consumer config]:proto file:_files -g "*.proto"' \
                '--message=[fully-qualified message name]:name:' \
                '--chain=[path to chain.pxf]:chain file:_files' \
                '--layers=[directory holding layer files]:dir:_files -/' \
                '--env=[environment to resolve]:env:' \
                '--field=[dotted field path to focus on]:path:' \
                '--ctx=[key=value pairs for ${ctx.*}]:ctx:' \
                '--help[show flags]'
            ;;
        verify)
            _arguments \
                '--chain=[path to chain.pxf]:chain file:_files' \
                '--layers=[directory holding layer files]:dir:_files -/' \
                '--env=[environment]:env:' \
                '--ctx=[key=value pairs]:ctx:' \
                '--strict[fail on missing content_sha256]' \
                '--help'
            ;;
        check-schema)
            _arguments \
                '*--schema=[.proto file, or directory to search (repeatable)]:proto file or dir:_files -g "*.proto"' \
                '--message=[optional: assert this message exists]:name:' \
                '*--include=[import search directory]:dir:_files -/' \
                '*--exclude=[skip files/dirs by name while searching a --schema directory]:pattern:' \
                '--help'
            ;;
        migrate)
            _arguments \
                '--chain=[path to chain.pxf]:chain file:_files' \
                '--layers=[directory]:dir:_files -/' \
                '--env=[environment]:env:' \
                '--ctx=[key=value pairs]:ctx:' \
                '--transform=[add-field|delete-field|rename-field]:t:(add-field delete-field rename-field)' \
                '--path=[dotted path]:path:' \
                '--from=[rename source]:path:' \
                '--to=[rename target]:path:' \
                '--value=[string value]:v:' \
                '--int=[int value]:v:' \
                '--bool=[bool value]:v:(true false)' \
                '--only=[restrict to one layer id]:id:' \
                '--dry-run[print intended writes]' \
                '--help'
            ;;
        hash)
            _files -g '*.pxf'
            ;;
        lint)
            # singlechecker driver — accepts Go package patterns
            # (./..., pkg/path) plus standard analysis flags.
            _files -/
            ;;
        completion)
            _values 'shell' bash zsh fish
            ;;
    esac
}
compdef _chameleon chameleon
`

const completionFish = `# chameleon fish completion
complete -c chameleon -f

# Top-level subcommands
complete -c chameleon -n '__fish_use_subcommand' -a explain    -d 'Resolve a config and print per-field provenance'
complete -c chameleon -n '__fish_use_subcommand' -a verify     -d 'Check @header.content_sha256 of every layer'
complete -c chameleon -n '__fish_use_subcommand' -a check-schema -d 'Check .proto files against the PXF bind-time rules'
complete -c chameleon -n '__fish_use_subcommand' -a migrate    -d 'Bulk-edit layer files'
complete -c chameleon -n '__fish_use_subcommand' -a hash       -d 'Print canonical body SHA-256'
complete -c chameleon -n '__fish_use_subcommand' -a lint       -d 'Flag pxfv1.Secret.Value reads / writes in consumer code'
complete -c chameleon -n '__fish_use_subcommand' -a version    -d 'Print chameleon version'
complete -c chameleon -n '__fish_use_subcommand' -a completion -d 'Print shell completion script'
complete -c chameleon -n '__fish_use_subcommand' -a help       -d 'Show top-level help'

# explain
complete -c chameleon -n '__fish_seen_subcommand_from explain' -l schema  -d '.proto file describing the consumer config' -r
complete -c chameleon -n '__fish_seen_subcommand_from explain' -l message -d 'Fully-qualified message name'
complete -c chameleon -n '__fish_seen_subcommand_from explain' -l chain   -d 'Path to chain.pxf' -r
complete -c chameleon -n '__fish_seen_subcommand_from explain' -l layers  -d 'Directory holding layer files' -r
complete -c chameleon -n '__fish_seen_subcommand_from explain' -l env     -d 'Environment to resolve'
complete -c chameleon -n '__fish_seen_subcommand_from explain' -l field   -d 'Dotted field path to focus on'
complete -c chameleon -n '__fish_seen_subcommand_from explain' -l ctx     -d 'key=value pairs for ${ctx.*}'

# verify
complete -c chameleon -n '__fish_seen_subcommand_from verify' -l chain   -d 'Path to chain.pxf' -r
complete -c chameleon -n '__fish_seen_subcommand_from verify' -l layers  -d 'Directory holding layer files' -r
complete -c chameleon -n '__fish_seen_subcommand_from verify' -l env     -d 'Environment'
complete -c chameleon -n '__fish_seen_subcommand_from verify' -l ctx     -d 'key=value pairs'
complete -c chameleon -n '__fish_seen_subcommand_from verify' -l strict  -d 'Fail on missing content_sha256'

# check-schema
complete -c chameleon -n '__fish_seen_subcommand_from check-schema' -l schema  -d '.proto file or directory to check (repeatable)' -r
complete -c chameleon -n '__fish_seen_subcommand_from check-schema' -l message -d 'Optional: assert this message exists'
complete -c chameleon -n '__fish_seen_subcommand_from check-schema' -l include -d 'Import search directory (repeatable)' -r
complete -c chameleon -n '__fish_seen_subcommand_from check-schema' -l exclude -d 'Skip files/dirs by name while searching (repeatable)' -r

# migrate
complete -c chameleon -n '__fish_seen_subcommand_from migrate' -l chain     -d 'Path to chain.pxf' -r
complete -c chameleon -n '__fish_seen_subcommand_from migrate' -l layers    -d 'Directory' -r
complete -c chameleon -n '__fish_seen_subcommand_from migrate' -l env       -d 'Environment'
complete -c chameleon -n '__fish_seen_subcommand_from migrate' -l ctx       -d 'key=value pairs'
complete -c chameleon -n '__fish_seen_subcommand_from migrate' -l transform -d 'Transform kind' -xa 'add-field delete-field rename-field'
complete -c chameleon -n '__fish_seen_subcommand_from migrate' -l path      -d 'Dotted path'
complete -c chameleon -n '__fish_seen_subcommand_from migrate' -l from      -d 'Rename source'
complete -c chameleon -n '__fish_seen_subcommand_from migrate' -l to        -d 'Rename target'
complete -c chameleon -n '__fish_seen_subcommand_from migrate' -l value     -d 'String value'
complete -c chameleon -n '__fish_seen_subcommand_from migrate' -l int       -d 'Int value'
complete -c chameleon -n '__fish_seen_subcommand_from migrate' -l bool      -d 'Bool value' -xa 'true false'
complete -c chameleon -n '__fish_seen_subcommand_from migrate' -l only      -d 'Restrict to layer id'
complete -c chameleon -n '__fish_seen_subcommand_from migrate' -l dry-run   -d 'Print intended writes only'

# hash
complete -c chameleon -n '__fish_seen_subcommand_from hash' -F -a '*.pxf'

# completion
complete -c chameleon -n '__fish_seen_subcommand_from completion' -xa 'bash zsh fish'
`
