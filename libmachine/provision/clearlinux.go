package provision

import (
	"bytes"
	"fmt"
	"text/template"

	"github.com/docker/machine/libmachine/auth"
	"github.com/docker/machine/libmachine/drivers"
	"github.com/docker/machine/libmachine/engine"
	"github.com/docker/machine/libmachine/log"
	"github.com/docker/machine/libmachine/provision/pkgaction"
	"github.com/docker/machine/libmachine/swarm"
	"github.com/docker/machine/libmachine/versioncmp"
	"github.com/docker/machine/libmachine/provision/serviceaction"
)

func init() {
	Register("ClearLinux", &RegisteredProvisioner{
		New: NewClearLinuxProvisioner,
	})
}

func NewClearLinuxProvisioner(d drivers.Driver) Provisioner {
	return &ClearLinuxProvisioner{
		NewSystemdProvisioner("clear-linux-os", d),
	}
}

type ClearLinuxProvisioner struct {
	SystemdProvisioner
}

func (provisioner *ClearLinuxProvisioner) String() string {
	return "ClearLinux"
}

func (provisioner *ClearLinuxProvisioner) SetHostname(hostname string) error {
	log.Debugf("SetHostname: %s", hostname)

	command := fmt.Sprintf("sudo hostnamectl set-hostname %s", hostname)
	if _, err := provisioner.SSHCommand(command); err != nil {
		return err
	}

	return nil
}

func (provisioner *ClearLinuxProvisioner) GenerateDockerOptions(dockerPort int) (*DockerOptions, error) {
	var (
		engineCfg bytes.Buffer
	)

	driverNameLabel := fmt.Sprintf("provider=%s", provisioner.Driver.DriverName())
	provisioner.EngineOptions.Labels = append(provisioner.EngineOptions.Labels, driverNameLabel)

	dockerVersion, err := DockerClientVersion(provisioner)
	if err != nil {
		return nil, err
	}

	arg := "daemon"
	if versioncmp.GreaterThanOrEqualTo(dockerVersion, "1.12.0") {
		arg = ""
	}

	engineConfigTmpl := `[Service]
Environment=TMPDIR=/var/tmp
ExecStart=
ExecStart=/usr/bin/dockerd ` + arg + ` --host=unix:///var/run/docker.sock --host=tcp://0.0.0.0:{{.DockerPort}} --tlsverify --tlscacert {{.AuthOptions.CaCertRemotePath}} --tlscert {{.AuthOptions.ServerCertRemotePath}} --tlskey {{.AuthOptions.ServerKeyRemotePath}}{{ range .EngineOptions.Labels }} --label {{.}}{{ end }}{{ range .EngineOptions.InsecureRegistry }} --insecure-registry {{.}}{{ end }}{{ range .EngineOptions.RegistryMirror }} --registry-mirror {{.}}{{ end }}{{ range .EngineOptions.ArbitraryFlags }} --{{.}}{{ end }} \$DOCKER_OPTS \$DOCKER_OPT_BIP \$DOCKER_OPT_MTU \$DOCKER_OPT_IPMASQ
Environment={{range .EngineOptions.Env}}{{ printf "%q" . }} {{end}}
`

	t, err := template.New("engineConfig").Parse(engineConfigTmpl)
	if err != nil {
		return nil, err
	}

	log.Info("Engine=%s", engineConfigTmpl)

	engineConfigContext := EngineConfigContext{
		DockerPort:    dockerPort,
		AuthOptions:   provisioner.AuthOptions,
		EngineOptions: provisioner.EngineOptions,
	}

	t.Execute(&engineCfg, engineConfigContext)

	return &DockerOptions{
		EngineOptions:     engineCfg.String(),
		EngineOptionsPath: provisioner.DaemonOptionsFile,
	}, nil
}

func (provisioner *ClearLinuxProvisioner) Package(name string, action pkgaction.PackageAction) error {
	var packageAction string

  switch action {
  case pkgaction.Install, pkgaction.Upgrade:
    packageAction = "bundle-add"
  case pkgaction.Remove:
  case pkgaction.Purge:
    packageAction = "bundle-remove"
  }

  switch name {
  case "docker":
    name = "containers-basic"
  }

  command := fmt.Sprintf("swupd %s %s ", packageAction, name)
  log.Debugf("package: action=%s name=%s", action.String(), name)

  return waitForLock(provisioner, command)
	return nil
}

func (provisioner *ClearLinuxProvisioner) Provision(swarmOptions swarm.Options, authOptions auth.Options, engineOptions engine.Options) error {
	provisioner.SwarmOptions = swarmOptions
	provisioner.AuthOptions = authOptions
	provisioner.EngineOptions = engineOptions

	if err := provisioner.SetHostname(provisioner.Driver.GetMachineName()); err != nil {
		return err
	}

	if err := makeDockerOptionsDir(provisioner); err != nil {
		return err
	}

	log.Debugf("installing base package: name=containers-basic")
	if err := provisioner.Package("containers-basic", pkgaction.Install); err != nil {
		return err
	}

	log.Debugf("Preparing certificates")
	provisioner.AuthOptions = setRemoteAuthOptions(provisioner)

	log.Debugf("Setting up certificates")
	if err := ConfigureAuth(provisioner); err != nil {
		return err
	}

	log.Debug("Configuring swarm")
	err := configureSwarm(provisioner, swarmOptions, provisioner.AuthOptions)
	if err != nil {
		return err
	}

	// enable in systemd
	log.Debug("Enabling docker in systemd")
	err = provisioner.Service("docker", serviceaction.Enable)
	return err
}
