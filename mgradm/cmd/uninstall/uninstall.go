// SPDX-FileCopyrightText: 2024 SUSE LLC
//
// SPDX-License-Identifier: Apache-2.0

package uninstall

import (
	"github.com/spf13/cobra"
	"github.com/uyuni-project/uyuni-tools/shared"
	"github.com/uyuni-project/uyuni-tools/shared/kubernetes"
	. "github.com/uyuni-project/uyuni-tools/shared/l10n"
	"github.com/uyuni-project/uyuni-tools/shared/types"
	"github.com/uyuni-project/uyuni-tools/shared/utils"
)

type uninstallFlags struct {
	Backend      string
	Force        bool
	PurgeVolumes bool
}

// NewCommand uninstall a server and optionally the corresponding volumes.
func NewCommand(globalFlags *types.GlobalFlags) *cobra.Command {
	uninstallCmd := &cobra.Command{
		Use:   "uninstall",
		Short: L("Uninstall a server"),
		Long: L(`Uninstall a server and optionally the corresponding volumes.
By default it will only print what would be done, use --force to actually remove.`) + kubernetes.UninstallHelp(),
		Args: cobra.ExactArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			var flags uninstallFlags
			return utils.CommandHelper(globalFlags, cmd, args, &flags, uninstall)
		},
	}
	uninstallCmd.Flags().BoolP("force", "f", false, L("Actually remove the server"))
	uninstallCmd.Flags().Bool("purgeVolumes", false, L("Also remove the volumes"))

	if utils.KubernetesBuilt {
		utils.AddBackendFlag(uninstallCmd)
	}

	return uninstallCmd
}

func uninstall(
	globalFlags *types.GlobalFlags,
	flags *uninstallFlags,
	cmd *cobra.Command,
	args []string,
) error {
	fn, err := shared.ChoosePodmanOrKubernetes(cmd.Flags(), uninstallForPodman, uninstallForKubernetes)
	if err != nil {
		return err
	}

	return fn(globalFlags, flags, cmd, args)
}
