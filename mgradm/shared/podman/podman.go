// SPDX-FileCopyrightText: 2024 SUSE LLC
//
// SPDX-License-Identifier: Apache-2.0

package podman

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	install_shared "github.com/uyuni-project/uyuni-tools/mgradm/cmd/install/shared"
	"github.com/uyuni-project/uyuni-tools/mgradm/shared/ssl"
	"github.com/uyuni-project/uyuni-tools/mgradm/shared/templates"
	adm_utils "github.com/uyuni-project/uyuni-tools/mgradm/shared/utils"
	"github.com/uyuni-project/uyuni-tools/shared"
	. "github.com/uyuni-project/uyuni-tools/shared/l10n"
	"github.com/uyuni-project/uyuni-tools/shared/podman"
	"github.com/uyuni-project/uyuni-tools/shared/types"
	"github.com/uyuni-project/uyuni-tools/shared/utils"
)

// GetExposedPorts returns the port exposed.
func GetExposedPorts(debug bool) []types.PortMap {
	ports := []types.PortMap{
		utils.NewPortMap("https", 443, 443),
		utils.NewPortMap("http", 80, 80),
	}
	ports = append(ports, utils.TCP_PORTS...)
	ports = append(ports, utils.UDP_PORTS...)

	if debug {
		ports = append(ports, utils.DEBUG_PORTS...)
	}

	return ports
}

// GenerateAttestationSystemdService creates the coco attestation systemd files.
func GenerateAttestationSystemdService(image string, db install_shared.DbFlags) error {
	attestationData := templates.AttestationServiceTemplateData{
		NamePrefix: "uyuni",
		Network:    podman.UyuniNetwork,
		Image:      image,
	}
	if err := utils.WriteTemplateToFile(attestationData, podman.GetServicePath(podman.ServerAttestationService), 0555, false); err != nil {
		return fmt.Errorf(L("failed to generate systemd service unit file: %s"), err)
	}

	environment := fmt.Sprintf(`Environment=UYUNI_IMAGE=%s
Environment=database_connection=jdbc:postgresql://uyuni-server.mgr.internal:%d/%s
Environment=database_user=%s
Environment=database_password=%s
	`, image, db.Port, db.Name, db.User, db.Password)
	if err := podman.GenerateSystemdConfFile(podman.ServerAttestationService, "Service", environment); err != nil {
		return fmt.Errorf(L("cannot generate systemd conf file: %s"), err)
	}

	return podman.ReloadDaemon(false)
}

// GenerateSystemdService creates a serverY systemd file.
func GenerateSystemdService(tz string, image string, debug bool, podmanArgs []string) error {
	if err := podman.SetupNetwork(); err != nil {
		return fmt.Errorf(L("cannot setup network: %s"), err)
	}

	log.Info().Msg(L("Enabling system service"))
	args := append(podman.GetCommonParams(), podmanArgs...)

	data := templates.PodmanServiceTemplateData{
		Volumes:    utils.ServerVolumeMounts,
		NamePrefix: "uyuni",
		Args:       strings.Join(args, " "),
		Ports:      GetExposedPorts(debug),
		Timezone:   tz,
		Network:    podman.UyuniNetwork,
	}
	if err := utils.WriteTemplateToFile(data, podman.GetServicePath("uyuni-server"), 0555, false); err != nil {
		return fmt.Errorf(L("failed to generate systemd service unit file: %s"), err)
	}

	if err := podman.GenerateSystemdConfFile("uyuni-server", "Service", "Environment=UYUNI_IMAGE="+image); err != nil {
		return fmt.Errorf(L("cannot generate systemd conf file: %s"), err)
	}
	return podman.ReloadDaemon(false)
}

// UpdateSslCertificate update SSL certificate.
func UpdateSslCertificate(cnx *shared.Connection, chain *ssl.CaChain, serverPair *ssl.SslPair) error {
	ssl.CheckPaths(chain, serverPair)

	// Copy the CAs, certificate and key to the container
	const certDir = "/tmp/uyuni-tools"
	if err := utils.RunCmd("podman", "exec", podman.ServerContainerName, "mkdir", "-p", certDir); err != nil {
		return fmt.Errorf(L("failed to create temporary folder on container to copy certificates to"))
	}

	rootCaPath := path.Join(certDir, "root-ca.crt")
	serverCrtPath := path.Join(certDir, "server.crt")
	serverKeyPath := path.Join(certDir, "server.key")

	log.Debug().Msgf("Intermediate CA flags: %v", chain.Intermediate)

	args := []string{
		"exec",
		podman.ServerContainerName,
		"mgr-ssl-cert-setup",
		"-vvv",
		"--root-ca-file", rootCaPath,
		"--server-cert-file", serverCrtPath,
		"--server-key-file", serverKeyPath,
	}

	if err := cnx.Copy(chain.Root, "server:"+rootCaPath, "root", "root"); err != nil {
		return fmt.Errorf(L("cannot copy %s: %s"), rootCaPath, err)
	}
	if err := cnx.Copy(serverPair.Cert, "server:"+serverCrtPath, "root", "root"); err != nil {
		return fmt.Errorf(L("cannot copy %s: %s"), serverCrtPath, err)
	}
	if err := cnx.Copy(serverPair.Key, "server:"+serverKeyPath, "root", "root"); err != nil {
		return fmt.Errorf(L("cannot copy %s: %s"), serverKeyPath, err)
	}

	for i, ca := range chain.Intermediate {
		caFilename := fmt.Sprintf("ca-%d.crt", i)
		caPath := path.Join(certDir, caFilename)
		args = append(args, "--intermediate-ca-file", caPath)
		if err := cnx.Copy(ca, "server:"+caPath, "root", "root"); err != nil {
			return fmt.Errorf(L("cannot copy %s: %s"), caPath, err)
		}
	}

	// Check and install then using mgr-ssl-cert-setup
	if _, err := utils.RunCmdOutput(zerolog.InfoLevel, "podman", args...); err != nil {
		return errors.New(L("failed to update SSL certificate"))
	}

	// Clean the copied files and the now useless ssl-build
	if err := utils.RunCmd("podman", "exec", podman.ServerContainerName, "rm", "-rf", certDir); err != nil {
		return errors.New(L("failed to remove copied certificate files in the container"))
	}

	const sslbuildPath = "/root/ssl-build"
	if cnx.TestExistenceInPod(sslbuildPath) {
		if err := utils.RunCmd("podman", "exec", podman.ServerContainerName, "rm", "-rf", sslbuildPath); err != nil {
			return errors.New(L("failed to remove now useless ssl-build folder in the container"))
		}
	}

	// The services need to be restarted
	log.Info().Msg(L("Restarting services after updating the certificate"))
	return utils.RunCmdStdMapping(zerolog.DebugLevel, "podman", "exec", podman.ServerContainerName, "spacewalk-service", "restart")
}

// RunMigration migrate an existing remote server to a container.
func RunMigration(serverImage string, pullPolicy string, sshAuthSocket string, sshConfigPath string, sshKnownhostsPath string, sourceFqdn string, user string) (string, string, string, error) {
	scriptDir, err := adm_utils.GenerateMigrationScript(sourceFqdn, user, false)
	if err != nil {
		return "", "", "", fmt.Errorf(L("cannot generate migration script: %s"), err)
	}
	defer os.RemoveAll(scriptDir)

	extraArgs := []string{
		"--security-opt", "label:disable",
		"-e", "SSH_AUTH_SOCK",
		"-v", filepath.Dir(sshAuthSocket) + ":" + filepath.Dir(sshAuthSocket),
		"-v", scriptDir + ":/var/lib/uyuni-tools/",
	}

	if sshConfigPath != "" {
		extraArgs = append(extraArgs, "-v", sshConfigPath+":/tmp/ssh_config")
	}

	if sshKnownhostsPath != "" {
		extraArgs = append(extraArgs, "-v", sshKnownhostsPath+":/etc/ssh/ssh_known_hosts")
	}

	inspectedHostValues, err := utils.InspectHost()
	if err != nil {
		return "", "", "", fmt.Errorf(L("cannot inspect host values: %s"), err)
	}

	pullArgs := []string{}
	_, scc_user_exist := inspectedHostValues["host_scc_username"]
	_, scc_user_password := inspectedHostValues["host_scc_password"]
	if scc_user_exist && scc_user_password {
		pullArgs = append(pullArgs, "--creds", inspectedHostValues["host_scc_username"]+":"+inspectedHostValues["host_scc_password"])
	}

	preparedImage, err := podman.PrepareImage(serverImage, pullPolicy, pullArgs...)
	if err != nil {
		return "", "", "", err
	}

	log.Info().Msg(L("Migrating server"))
	if err := podman.RunContainer("uyuni-migration", preparedImage, extraArgs,
		[]string{"/var/lib/uyuni-tools/migrate.sh"}); err != nil {
		return "", "", "", fmt.Errorf(L("cannot run uyuni migration container: %s"), err)
	}
	tz, oldPgVersion, newPgVersion, err := adm_utils.ReadContainerData(scriptDir)

	if err != nil {
		return "", "", "", fmt.Errorf(L("cannot read extracted data: %s"), err)
	}

	return tz, oldPgVersion, newPgVersion, nil
}

// RunPgsqlVersionUpgrade perform a PostgreSQL major upgrade.
func RunPgsqlVersionUpgrade(image types.ImageFlags, migrationImage types.ImageFlags, oldPgsql string, newPgsql string) error {
	log.Info().Msgf(L("Previous PostgreSQL is %s, new one is %s. Performing a DB version upgrade..."), oldPgsql, newPgsql)

	scriptDir, err := os.MkdirTemp("", "mgradm-*")
	defer os.RemoveAll(scriptDir)
	if err != nil {
		return fmt.Errorf(L("failed to create temporary directory: %s"), err)
	}
	if newPgsql > oldPgsql {
		pgsqlVersionUpgradeContainer := "uyuni-upgrade-pgsql"
		extraArgs := []string{
			"-v", scriptDir + ":/var/lib/uyuni-tools/",
			"--security-opt", "label:disable",
		}

		migrationImageUrl := ""
		if migrationImage.Name == "" {
			migrationImageUrl, err = utils.ComputeImage(image.Name, image.Tag, fmt.Sprintf("-migration-%s-%s", oldPgsql, newPgsql))
			if err != nil {
				return fmt.Errorf(L("failed to compute image URL: %s"), err)
			}
		} else {
			migrationImageUrl, err = utils.ComputeImage(migrationImage.Name, image.Tag)
			if err != nil {
				return fmt.Errorf(L("failed to compute image URL: %s"), err)
			}
		}

		inspectedHostValues, err := utils.InspectHost()
		if err != nil {
			return fmt.Errorf(L("cannot inspect host values: %s"), err)
		}

		pullArgs := []string{}
		_, scc_user_exist := inspectedHostValues["host_scc_username"]
		_, scc_user_password := inspectedHostValues["host_scc_password"]
		if scc_user_exist && scc_user_password {
			pullArgs = append(pullArgs, "--creds", inspectedHostValues["host_scc_username"]+":"+inspectedHostValues["host_scc_password"])
		}

		preparedImage, err := podman.PrepareImage(migrationImageUrl, image.PullPolicy, pullArgs...)
		if err != nil {
			return err
		}

		log.Info().Msgf(L("Using migration image %s"), preparedImage)

		pgsqlVersionUpgradeScriptName, err := adm_utils.GeneratePgsqlVersionUpgradeScript(scriptDir, oldPgsql, newPgsql, false)
		if err != nil {
			return fmt.Errorf(L("cannot generate PostgreSQL database version upgrade script %s"), err)
		}

		err = podman.RunContainer(pgsqlVersionUpgradeContainer, preparedImage, extraArgs,
			[]string{"/var/lib/uyuni-tools/" + pgsqlVersionUpgradeScriptName})
		if err != nil {
			return err
		}
	}
	return nil
}

// RunPgsqlFinalizeScript run the script with all the action required to a db after upgrade.
func RunPgsqlFinalizeScript(serverImage string, schemaUpdateRequired bool) error {
	scriptDir, err := os.MkdirTemp("", "mgradm-*")
	defer os.RemoveAll(scriptDir)
	if err != nil {
		return fmt.Errorf(L("failed to create temporary directory: %s"), err)
	}

	extraArgs := []string{
		"-v", scriptDir + ":/var/lib/uyuni-tools/",
		"--security-opt", "label:disable",
	}
	pgsqlFinalizeContainer := "uyuni-finalize-pgsql"
	pgsqlFinalizeScriptName, err := adm_utils.GenerateFinalizePostgresScript(scriptDir, true, schemaUpdateRequired, true, true, false)
	if err != nil {
		return fmt.Errorf(L("cannot generate PostgreSQL finalization script: %s"), err)
	}
	err = podman.RunContainer(pgsqlFinalizeContainer, serverImage, extraArgs,
		[]string{"/var/lib/uyuni-tools/" + pgsqlFinalizeScriptName})
	if err != nil {
		return err
	}
	return nil
}

// RunPostUpgradeScript run the script with the changes to apply after the upgrade.
func RunPostUpgradeScript(serverImage string) error {
	scriptDir, err := os.MkdirTemp("", "mgradm-*")
	defer os.RemoveAll(scriptDir)
	if err != nil {
		return fmt.Errorf(L("failed to create temporary directory: %s"), err)
	}
	postUpgradeContainer := "uyuni-post-upgrade"
	extraArgs := []string{
		"-v", scriptDir + ":/var/lib/uyuni-tools/",
		"--security-opt", "label:disable",
	}
	postUpgradeScriptName, err := adm_utils.GeneratePostUpgradeScript(scriptDir, "localhost")
	if err != nil {
		return fmt.Errorf(L("cannot generate PostgreSQL finalization script: %s"), err)
	}
	err = podman.RunContainer(postUpgradeContainer, serverImage, extraArgs,
		[]string{"/var/lib/uyuni-tools/" + postUpgradeScriptName})
	if err != nil {
		return err
	}
	return nil
}

// Upgrade will upgrade server to the image given as attribute.
func Upgrade(image types.ImageFlags, migrationImage types.ImageFlags, args []string) error {
	serverImage, err := utils.ComputeImage(image.Name, image.Tag)
	if err != nil {
		return fmt.Errorf(L("failed to compute image URL"))
	}

	inspectedValues, err := Inspect(serverImage, image.PullPolicy)
	if err != nil {
		return fmt.Errorf(L("cannot inspect podman values: %s"), err)
	}

	cnx := shared.NewConnection("podman", podman.ServerContainerName, "")

	if err := adm_utils.SanityCheck(cnx, inspectedValues, serverImage); err != nil {
		return err
	}

	if err := podman.StopService(podman.ServerService); err != nil {
		return fmt.Errorf(L("cannot stop service %s"), err)
	}

	defer func() {
		err = podman.StartService(podman.ServerService)
	}()
	if inspectedValues["image_pg_version"] > inspectedValues["current_pg_version"] {
		log.Info().Msgf(L("Previous postgresql is %s, instead new one is %s. Performing a DB version upgrade..."), inspectedValues["current_pg_version"], inspectedValues["image_pg_version"])
		if err := RunPgsqlVersionUpgrade(image, migrationImage, inspectedValues["current_pg_version"], inspectedValues["image_pg_version"]); err != nil {
			return fmt.Errorf(L("cannot run PostgreSQL version upgrade script: %s"), err)
		}
	} else if inspectedValues["image_pg_version"] == inspectedValues["current_pg_version"] {
		log.Info().Msgf(L("Upgrading to %s without changing PostgreSQL version"), inspectedValues["uyuni_release"])
	} else {
		return fmt.Errorf(L("trying to downgrade postgresql from %s to %s"), inspectedValues["current_pg_version"], inspectedValues["image_pg_version"])
	}

	schemaUpdateRequired := inspectedValues["current_pg_version"] != inspectedValues["image_pg_version"]
	if err := RunPgsqlFinalizeScript(serverImage, schemaUpdateRequired); err != nil {
		return fmt.Errorf(L("cannot run PostgreSQL version upgrade script: %s"), err)
	}

	if err := RunPostUpgradeScript(serverImage); err != nil {
		return fmt.Errorf(L("cannot run post upgrade script: %s"), err)
	}

	if err := podman.GenerateSystemdConfFile("uyuni-server", "Service", "Environment=UYUNI_IMAGE="+serverImage); err != nil {
		return err
	}
	log.Info().Msg(L("Waiting for the server to start..."))
	return podman.ReloadDaemon(false)
}

// Inspect check values on a given image and deploy.
func Inspect(serverImage string, pullPolicy string) (map[string]string, error) {
	scriptDir, err := os.MkdirTemp("", "mgradm-*")
	defer os.RemoveAll(scriptDir)
	if err != nil {
		return map[string]string{}, fmt.Errorf(L("failed to create temporary directory %s"), err)
	}

	inspectedHostValues, err := utils.InspectHost()
	if err != nil {
		return map[string]string{}, fmt.Errorf(L("cannot inspect host values: %s"), err)
	}

	pullArgs := []string{}
	_, scc_user_exist := inspectedHostValues["host_scc_username"]
	_, scc_user_password := inspectedHostValues["host_scc_password"]
	if scc_user_exist && scc_user_password {
		pullArgs = append(pullArgs, "--creds", inspectedHostValues["host_scc_username"]+":"+inspectedHostValues["host_scc_password"])
	}

	preparedImage, err := podman.PrepareImage(serverImage, pullPolicy, pullArgs...)
	if err != nil {
		return map[string]string{}, err
	}

	if err := utils.GenerateInspectContainerScript(scriptDir); err != nil {
		return map[string]string{}, err
	}

	podmanArgs := []string{
		"-v", scriptDir + ":" + utils.InspectOutputFile.Directory,
		"--security-opt", "label:disable",
	}

	err = podman.RunContainer("uyuni-inspect", preparedImage, podmanArgs,
		[]string{utils.InspectOutputFile.Directory + "/" + utils.InspectScriptFilename})
	if err != nil {
		return map[string]string{}, err
	}

	inspectResult, err := utils.ReadInspectData(scriptDir)
	if err != nil {
		return map[string]string{}, fmt.Errorf(L("cannot inspect data. %s"), err)
	}

	return inspectResult, err
}
