package cmd

import "github.com/spf13/cobra"

func registerOutputFlagCompletion(cmd *cobra.Command) {
	_ = cmd.RegisterFlagCompletionFunc("output", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return []string{"human", "json", "yaml"}, cobra.ShellCompDirectiveNoFileComp
	})
}

func registerSourceFlagCompletion(cmd *cobra.Command) {
	_ = cmd.RegisterFlagCompletionFunc("source", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return []string{"managed", "local", "controlplane", "all"}, cobra.ShellCompDirectiveNoFileComp
	})
}

func registerDeprovisionScopeCompletion(cmd *cobra.Command) {
	_ = cmd.RegisterFlagCompletionFunc("scope", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return []string{"all", "local"}, cobra.ShellCompDirectiveNoFileComp
	})
}

func registerSystemPruneModeCompletion(cmd *cobra.Command) {
	_ = cmd.RegisterFlagCompletionFunc("mode", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return []string{"dangling", "containers", "volumes", "all"}, cobra.ShellCompDirectiveNoFileComp
	})
}

func registerImagePruneModeCompletion(cmd *cobra.Command) {
	_ = cmd.RegisterFlagCompletionFunc("mode", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return []string{"dangling"}, cobra.ShellCompDirectiveNoFileComp
	})
}

func registerModelPruneModeCompletion(cmd *cobra.Command) {
	_ = cmd.RegisterFlagCompletionFunc("mode", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return []string{"dangling"}, cobra.ShellCompDirectiveNoFileComp
	})
}

func registerModelFormatCompletion(cmd *cobra.Command) {
	_ = cmd.RegisterFlagCompletionFunc("format", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return []string{"gguf", "safetensors", "onnx", "pytorch", "tensorrt", "unknown"}, cobra.ShellCompDirectiveNoFileComp
	})
}
