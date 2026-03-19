package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

type skillTarget struct {
	Name string // "Claude Code", "Codex CLI", "Cross-agent"
	Dir  string // full path to skills/mcpmu/
}

var skillCmd = &cobra.Command{
	Use:   "skill",
	Short: "Manage mcpmu agent skill",
	Long:  `Install or uninstall the mcpmu agent skill for AI coding agents.`,
}

var skillInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Install mcpmu skill to detected agents",
	Long: `Auto-detect installed AI coding agents and install the mcpmu skill.

The skill is always installed to ~/.agents/skills/mcpmu/ (cross-agent standard).
It is also installed to agent-specific paths when those agents are detected:
  - Claude Code: ~/.claude/skills/mcpmu/
  - Codex CLI:   ~/.codex/skills/mcpmu/

Examples:
  mcpmu skill install`,
	RunE: runSkillInstall,
}

var skillUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Uninstall mcpmu skill from all agents",
	Long: `Remove the mcpmu skill from all known agent locations.

Only removes files created by mcpmu. The mcpmu/ directory is removed
only if empty after deleting SKILL.md.

Examples:
  mcpmu skill uninstall`,
	RunE: runSkillUninstall,
}

func init() {
	rootCmd.AddCommand(skillCmd)
	skillCmd.AddCommand(skillInstallCmd)
	skillCmd.AddCommand(skillUninstallCmd)
}

func detectSkillTargets(homeDir string) []skillTarget {
	var targets []skillTarget

	if info, err := os.Stat(filepath.Join(homeDir, ".claude")); err == nil && info.IsDir() {
		targets = append(targets, skillTarget{
			Name: "Claude Code",
			Dir:  filepath.Join(homeDir, ".claude", "skills", "mcpmu"),
		})
	}

	if info, err := os.Stat(filepath.Join(homeDir, ".codex")); err == nil && info.IsDir() {
		targets = append(targets, skillTarget{
			Name: "Codex CLI",
			Dir:  filepath.Join(homeDir, ".codex", "skills", "mcpmu"),
		})
	}

	// Cross-agent standard — always included
	targets = append(targets, skillTarget{
		Name: "Cross-agent",
		Dir:  filepath.Join(homeDir, ".agents", "skills", "mcpmu"),
	})

	return targets
}

type skillResult struct {
	Path string
	Err  error
}

func installSkill(targets []skillTarget, content []byte) (installed []string, errs []skillResult) {
	for _, t := range targets {
		skillPath := filepath.Join(t.Dir, "SKILL.md")
		if err := os.MkdirAll(t.Dir, 0755); err != nil {
			errs = append(errs, skillResult{Path: skillPath, Err: err})
			continue
		}
		if err := os.WriteFile(skillPath, content, 0644); err != nil {
			errs = append(errs, skillResult{Path: skillPath, Err: err})
			continue
		}
		installed = append(installed, skillPath)
	}
	return installed, errs
}

func uninstallSkill(targets []skillTarget) (removed []string, errs []skillResult) {
	for _, t := range targets {
		skillPath := filepath.Join(t.Dir, "SKILL.md")
		if _, err := os.Stat(skillPath); os.IsNotExist(err) {
			continue
		}
		if err := os.Remove(skillPath); err != nil {
			errs = append(errs, skillResult{Path: skillPath, Err: err})
			continue
		}
		removed = append(removed, skillPath)
		// Remove parent dir only if empty
		_ = os.Remove(t.Dir)
	}
	return removed, errs
}

func tildePath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if strings.HasPrefix(path, home) {
		return "~" + path[len(home):]
	}
	return path
}

func runSkillInstall(_ *cobra.Command, _ []string) error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("cannot determine home directory: %w", err)
	}

	targets := detectSkillTargets(homeDir)
	installed, errs := installSkill(targets, embeddedSkillMD)

	if len(installed) > 0 {
		fmt.Println("Installed mcpmu skill to:")
		for _, p := range installed {
			fmt.Printf("  %s\n", tildePath(p))
		}
	}

	if len(errs) > 0 {
		if len(installed) > 0 {
			fmt.Println()
		}
		fmt.Println("Errors:")
		for _, e := range errs {
			fmt.Printf("  %s: %s\n", tildePath(e.Path), e.Err)
		}
		os.Exit(1)
	}

	return nil
}

func runSkillUninstall(_ *cobra.Command, _ []string) error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("cannot determine home directory: %w", err)
	}

	targets := detectSkillTargets(homeDir)
	removed, errs := uninstallSkill(targets)

	if len(removed) > 0 {
		fmt.Println("Removed mcpmu skill from:")
		for _, p := range removed {
			fmt.Printf("  %s\n", tildePath(p))
		}
	} else if len(errs) == 0 {
		fmt.Println("No mcpmu skill files found to remove.")
	}

	if len(errs) > 0 {
		if len(removed) > 0 {
			fmt.Println()
		}
		fmt.Println("Errors:")
		for _, e := range errs {
			fmt.Printf("  %s: %s\n", tildePath(e.Path), e.Err)
		}
		os.Exit(1)
	}

	return nil
}
