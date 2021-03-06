package component

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"

	devfilev1 "github.com/devfile/api/v2/pkg/apis/workspaces/v1alpha2"
	"github.com/devfile/library/pkg/devfile"
	"github.com/openshift/odo/pkg/devfile/validate"
	"github.com/openshift/odo/pkg/envinfo"
	"github.com/openshift/odo/pkg/localConfigProvider"
	"github.com/openshift/odo/pkg/machineoutput"
	"github.com/openshift/odo/pkg/odo/genericclioptions"
	"github.com/openshift/odo/pkg/odo/util/pushtarget"
	"github.com/openshift/odo/pkg/util"
	"github.com/pkg/errors"

	"github.com/openshift/odo/pkg/devfile/adapters"
	"github.com/openshift/odo/pkg/devfile/adapters/common"
	"github.com/openshift/odo/pkg/devfile/adapters/kubernetes"
	"github.com/openshift/odo/pkg/log"
)

/*
Devfile support is an experimental feature which extends the support for the
use of Che devfiles in odo for performing various odo operations.

The devfile support progress can be tracked by:
https://github.com/openshift/odo/issues/2467

Please note that this feature is currently under development,
the feature will be available with experimental mode enabled.

The behaviour of this feature is subject to change as development for this
feature progresses.
*/

// Constants for devfile component
const (
	devFile = "devfile.yaml"
)

// DevfilePath is the devfile path that is used by odo,
// which means odo can:
// 1. Directly use the devfile in DevfilePath
// 2. Download devfile from registry to DevfilePath then use the devfile in DevfilePath
// 3. Copy user's own devfile (path is specified via --devfile flag) to DevfilePath then use the devfile in DevfilePath
var DevfilePath = filepath.Join(LocalDirectoryDefaultLocation, devFile)

// DevfilePush has the logic to perform the required actions for a given devfile
func (po *PushOptions) DevfilePush() error {

	// Wrap the push so that we can capture the error in JSON-only mode
	err := po.devfilePushInner()

	if err != nil && log.IsJSON() {
		eventLoggingClient := machineoutput.NewConsoleMachineEventLoggingClient()
		eventLoggingClient.ReportError(err, machineoutput.TimestampNow())

		// Suppress the error to prevent it from being output by the generic machine-readable handler (which will produce invalid JSON for our purposes)
		err = nil

		// os.Exit(1) since we are suppressing the generic machine-readable handler's exit code logic
		os.Exit(1)
	}

	if err != nil {
		return err
	}

	// push is successful, save the runMode used
	runMode := envinfo.Run
	if po.debugRun {
		runMode = envinfo.Debug
	}

	return po.EnvSpecificInfo.SetRunMode(runMode)
}

func (po *PushOptions) devfilePushInner() (err error) {

	// Parse devfile and validate
	devObj, err := devfile.ParseAndValidate(po.DevfilePath)

	if err != nil {
		return err
	}

	err = validate.ValidateDevfileData(devObj.Data)
	if err != nil {
		return err
	}

	componentName := po.EnvSpecificInfo.GetName()

	// Set the source path to either the context or current working directory (if context not set)
	po.sourcePath, err = util.GetAbsPath(po.componentContext)
	if err != nil {
		return errors.Wrap(err, "unable to get source path")
	}

	// Apply ignore information
	err = genericclioptions.ApplyIgnore(&po.ignores, po.sourcePath)
	if err != nil {
		return errors.Wrap(err, "unable to apply ignore information")
	}

	var platformContext interface{}
	if pushtarget.IsPushTargetDocker() {
		platformContext = nil
	} else {
		kc := kubernetes.KubernetesContext{
			Namespace: po.KClient.Namespace,
		}
		platformContext = kc
	}

	devfileHandler, err := adapters.NewComponentAdapter(componentName, po.componentContext, po.Application, devObj, platformContext)
	if err != nil {
		return err
	}

	pushParams := common.PushParameters{
		Path:            po.sourcePath,
		IgnoredFiles:    po.ignores,
		ForceBuild:      po.forceBuild,
		Show:            po.show,
		EnvSpecificInfo: *po.EnvSpecificInfo,
		DevfileBuildCmd: strings.ToLower(po.devfileBuildCommand),
		DevfileRunCmd:   strings.ToLower(po.devfileRunCommand),
		DevfileDebugCmd: strings.ToLower(po.devfileDebugCommand),
		Debug:           po.debugRun,
		DebugPort:       po.EnvSpecificInfo.GetDebugPort(),
	}

	localURLs, err := po.EnvSpecificInfo.ListURLs()
	if err != nil {
		return err
	}
	warnIfURLSInvalid(localURLs)

	// Start or update the component
	err = devfileHandler.Push(pushParams)
	if err != nil {
		err = errors.Errorf("Failed to start component with name %s. Error: %v",
			componentName,
			err,
		)
	} else {
		log.Infof("\nPushing devfile component %s", componentName)
		log.Success("Changes successfully pushed to component")
	}

	return
}

// DevfileComponentLog fetch and display log from devfile components
func (lo LogOptions) DevfileComponentLog() error {
	// Parse devfile
	devObj, err := devfile.ParseAndValidate(lo.devfilePath)
	if err != nil {
		return err
	}
	err = validate.ValidateDevfileData(devObj.Data)
	if err != nil {
		return err
	}
	componentName := lo.Context.EnvSpecificInfo.GetName()

	var platformContext interface{}
	if pushtarget.IsPushTargetDocker() {
		platformContext = nil
	} else {
		kc := kubernetes.KubernetesContext{
			Namespace: lo.KClient.Namespace,
		}
		platformContext = kc
	}

	devfileHandler, err := adapters.NewComponentAdapter(componentName, lo.componentContext, lo.Application, devObj, platformContext)

	if err != nil {
		return err
	}

	var command devfilev1.Command
	if lo.debug {
		command, err = common.GetDebugCommand(devObj.Data, "")
		if err != nil {
			return err
		}
		if reflect.DeepEqual(devfilev1.Command{}, command) {
			return errors.Errorf("no debug command found in devfile, please run \"odo log\" for run command logs")
		}

	} else {
		command, err = common.GetRunCommand(devObj.Data, "")
		if err != nil {
			return err
		}
	}

	// Start or update the component
	rd, err := devfileHandler.Log(lo.logFollow, command)
	if err != nil {
		log.Errorf(
			"Failed to log component with name %s.\nError: %v",
			componentName,
			err,
		)
		os.Exit(1)
	}

	return util.DisplayLog(lo.logFollow, rd, os.Stdout, componentName, -1)
}

// DevfileComponentDelete deletes the devfile component
func (do *DeleteOptions) DevfileComponentDelete() error {
	// Parse devfile and validate
	devObj, err := devfile.ParseAndValidate(do.devfilePath)
	if err != nil {
		return err
	}
	err = validate.ValidateDevfileData(devObj.Data)
	if err != nil {
		return err
	}
	componentName := do.EnvSpecificInfo.GetName()

	kc := kubernetes.KubernetesContext{
		Namespace: do.namespace,
	}

	labels := map[string]string{
		"component": componentName,
	}
	devfileHandler, err := adapters.NewComponentAdapter(componentName, do.componentContext, do.Application, devObj, kc)
	if err != nil {
		return err
	}

	return devfileHandler.Delete(labels, do.show)
}

// RunTestCommand runs the specific test command in devfile
func (to *TestOptions) RunTestCommand() error {
	componentName := to.Context.EnvSpecificInfo.GetName()

	var platformContext interface{}
	if pushtarget.IsPushTargetDocker() {
		platformContext = nil
	} else {
		kc := kubernetes.KubernetesContext{
			Namespace: to.KClient.Namespace,
		}
		platformContext = kc
	}

	devfileHandler, err := adapters.NewComponentAdapter(componentName, to.componentContext, to.Application, to.devObj, platformContext)
	if err != nil {
		return err
	}
	return devfileHandler.Test(to.commandName, to.show)
}

func warnIfURLSInvalid(url []localConfigProvider.LocalURL) {
	// warnIfURLSInvalid checks if env.yaml contains a valid URL for the current pushtarget
	// display a warning if no url(s) found for the current push target, but found url(s) for another push target
	dockerURLExists := false
	kubeURLExists := false
	for _, element := range url {
		if element.Kind == localConfigProvider.DOCKER {
			dockerURLExists = true
		} else {
			kubeURLExists = true
		}
	}
	var urlOutput string
	if len(url) > 1 {
		urlOutput = "URLs"
	} else {
		urlOutput = "a URL"
	}
	if pushtarget.IsPushTargetDocker() && !dockerURLExists && kubeURLExists {
		log.Warningf("Found %v defined for Kubernetes, but no valid URLs for Docker.", urlOutput)
	} else if !pushtarget.IsPushTargetDocker() && !kubeURLExists && dockerURLExists {
		log.Warningf("Found %v defined for Docker, but no valid URLs for Kubernetes.", urlOutput)
	}
}

// DevfileComponentExec executes the given user command inside the component
func (eo *ExecOptions) DevfileComponentExec(command []string) error {
	// Parse devfile
	devObj, err := devfile.ParseAndValidate(eo.devfilePath)
	if err != nil {
		return err
	}
	err = validate.ValidateDevfileData(devObj.Data)
	if err != nil {
		return err
	}

	componentName := eo.componentOptions.EnvSpecificInfo.GetName()

	kc := kubernetes.KubernetesContext{
		Namespace: eo.namespace,
	}

	devfileHandler, err := adapters.NewComponentAdapter(componentName, eo.componentContext, eo.componentOptions.Application, devObj, kc)
	if err != nil {
		return err
	}

	return devfileHandler.Exec(command)
}
