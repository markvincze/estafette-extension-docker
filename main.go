package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"

	"github.com/alecthomas/kingpin"
	contracts "github.com/estafette/estafette-ci-contracts"
)

var (
	version   string
	branch    string
	revision  string
	buildDate string
	goVersion = runtime.Version()
)

var (
	// flags
	action       = kingpin.Flag("action", "Any of the following actions: build, push, tag.").Envar("ESTAFETTE_EXTENSION_ACTION").String()
	repositories = kingpin.Flag("repositories", "List of the repositories the image needs to be pushed to or tagged in.").Envar("ESTAFETTE_EXTENSION_REPOSITORIES").String()
	container    = kingpin.Flag("container", "Name of the container to build, defaults to app label if present.").Envar("ESTAFETTE_EXTENSION_CONTAINER").String()
	tags         = kingpin.Flag("tags", "List of tags the image needs to receive.").Envar("ESTAFETTE_EXTENSION_TAGS").String()
	path         = kingpin.Flag("path", "Directory to build docker container from, defaults to current working directory.").Default(".").OverrideDefaultFromEnvar("ESTAFETTE_EXTENSION_PATH").String()
	dockerfile   = kingpin.Flag("dockerfile", "Dockerfile to build, defaults to Dockerfile.").Default("Dockerfile").OverrideDefaultFromEnvar("ESTAFETTE_EXTENSION_DOCKERFILE").String()
	copy         = kingpin.Flag("copy", "List of files or directories to copy into the build directory.").Envar("ESTAFETTE_EXTENSION_COPY").String()
	args         = kingpin.Flag("args", "List of build arguments to pass to the build.").Envar("ESTAFETTE_EXTENSION_ARGS").String()
)

func main() {

	// parse command line parameters
	kingpin.Parse()

	// log to stdout and hide timestamp
	log.SetOutput(os.Stdout)
	log.SetFlags(log.Flags() &^ (log.Ldate | log.Ltime))

	// log startup message
	log.Printf("Starting estafette-extension-docker version %v...", version)

	// set defaults
	appLabel := os.Getenv("ESTAFETTE_LABEL_APP")
	if *container == "" && appLabel != "" {
		*container = appLabel
	}

	// get private container registries credentials
	credentialsJSON := os.Getenv("ESTAFETTE_CI_REPOSITORY_CREDENTIALS_JSON")
	var credentials []*contracts.ContainerRepositoryCredentialConfig
	if credentialsJSON != "" {
		json.Unmarshal([]byte(credentialsJSON), &credentials)
	}

	// validate inputs
	validateRepositories(*repositories)

	// split into arrays and set other variables
	var repositoriesSlice []string
	if *repositories != "" {
		repositoriesSlice = strings.Split(*repositories, ",")
	}
	var tagsSlice []string
	if *tags != "" {
		tagsSlice = strings.Split(*tags, ",")
	}
	var copySlice []string
	if *copy != "" {
		copySlice = strings.Split(*copy, ",")
	}
	var argsSlice []string
	if *args != "" {
		argsSlice = strings.Split(*args, ",")
	}
	estafetteBuildVersion := os.Getenv("ESTAFETTE_BUILD_VERSION")
	estafetteBuildVersionAsTag := tidyBuildVersionAsTag(estafetteBuildVersion)

	switch *action {
	case "build":

		// minimal using defaults

		// image: extensions/docker:stable
		// action: build
		// repositories:
		// - extensions

		// with defaults:

		// path: .
		// container: ${ESTAFETTE_LABEL_APP}
		// dockerfile: Dockerfile

		// or use a more verbose version to override defaults

		// image: extensions/docker:stable
		// env: SOME_BUILD_ARG_ENVVAR
		// action: build
		// container: docker
		// dockerfile: Dockerfile
		// repositories:
		// - extensions
		// path: .
		// copy:
		// - Dockerfile
		// - /etc/ssl/certs/ca-certificates.crt
		// args:
		// - SOME_BUILD_ARG_ENVVAR

		// make build dir if it doesn't exist
		log.Printf("Ensuring build directory %v exists\n", *path)
		runCommand("mkdir", []string{"-p", *path})

		// add dockerfile to items to copy if path is non-default and dockerfile isn't in the list to copy already
		if *path != "." && !contains(copySlice, *dockerfile) {
			copySlice = append(copySlice, *dockerfile)
		}

		// copy files/dirs from copySlice to build path
		for _, c := range copySlice {
			log.Printf("Copying %v to %v\n", c, *path)
			runCommand("cp", []string{"-r", c, *path})
		}

		// todo - check FROM statement to see whether login is required
		containerPath := fmt.Sprintf("%v/%v:%v", repositoriesSlice[0], *container, estafetteBuildVersionAsTag)
		loginIfRequired(credentials, containerPath)

		// build docker image
		log.Printf("Building docker image %v...\n", containerPath)
		args := []string{
			"build",
		}
		for _, r := range repositoriesSlice {
			args = append(args, "--tag")
			args = append(args, fmt.Sprintf("%v/%v:%v", r, *container, estafetteBuildVersionAsTag))
			for _, t := range tagsSlice {
				args = append(args, "--tag")
				args = append(args, fmt.Sprintf("%v/%v:%v", r, *container, t))
			}
		}
		for _, a := range argsSlice {
			argValue := os.Getenv(a)
			args = append(args, "--build-arg")
			args = append(args, fmt.Sprintf("%v=%v", a, argValue))
		}

		args = append(args, "--file")
		args = append(args, fmt.Sprintf("%v/%v", *path, *dockerfile))
		args = append(args, *path)
		runCommand("docker", args)

	case "push":

		// image: extensions/docker:stable
		// action: push
		// container: docker
		// repositories:
		// - extensions
		// tags:
		// - dev

		sourceContainerPath := fmt.Sprintf("%v/%v:%v", repositoriesSlice[0], *container, estafetteBuildVersionAsTag)

		// push each repository + tag combination
		for i, r := range repositoriesSlice {

			targetContainerPath := fmt.Sprintf("%v/%v:%v", r, *container, estafetteBuildVersionAsTag)

			if i > 0 {
				// tag container with default tag (it already exists for the first repository)
				log.Printf("Tagging container image %v\n", targetContainerPath)
				tagArgs := []string{
					"tag",
					sourceContainerPath,
					targetContainerPath,
				}
				err := exec.Command("docker", tagArgs...).Run()
				handleError(err)
			}

			loginIfRequired(credentials, targetContainerPath)

			// push container with default tag
			log.Printf("Pushing container image %v\n", targetContainerPath)
			pushArgs := []string{
				"push",
				targetContainerPath,
			}
			runCommand("docker", pushArgs)

			// push additional tags
			for _, t := range tagsSlice {

				targetContainerPath := fmt.Sprintf("%v/%v:%v", r, *container, t)

				// tag container with additional tag
				log.Printf("Tagging container image %v\n", targetContainerPath)
				tagArgs := []string{
					"tag",
					sourceContainerPath,
					targetContainerPath,
				}
				runCommand("docker", tagArgs)

				loginIfRequired(credentials, targetContainerPath)

				log.Printf("Pushing container image %v\n", targetContainerPath)
				pushArgs := []string{
					"push",
					targetContainerPath,
				}
				runCommand("docker", pushArgs)
			}
		}

	case "tag":

		// image: extensions/docker:stable
		// action: tag
		// container: docker
		// repositories:
		// - extensions
		// tags:
		// - stable
		// - latest

		sourceContainerPath := fmt.Sprintf("%v/%v:%v", repositoriesSlice[0], *container, estafetteBuildVersionAsTag)

		loginIfRequired(credentials, sourceContainerPath)

		// pull source container first
		log.Printf("Pulling container image %v\n", sourceContainerPath)
		pullArgs := []string{
			"pull",
			sourceContainerPath,
		}
		runCommand("docker", pullArgs)

		// push each repository + tag combination
		for i, r := range repositoriesSlice {

			targetContainerPath := fmt.Sprintf("%v/%v:%v", r, *container, estafetteBuildVersionAsTag)

			if i > 0 {
				// tag container with default tag
				log.Printf("Tagging container image %v\n", targetContainerPath)
				tagArgs := []string{
					"tag",
					sourceContainerPath,
					targetContainerPath,
				}
				runCommand("docker", tagArgs)

				loginIfRequired(credentials, targetContainerPath)

				// push container with default tag
				log.Printf("Pushing container image %v\n", targetContainerPath)
				pushArgs := []string{
					"push",
					targetContainerPath,
				}
				runCommand("docker", pushArgs)
			}

			// push additional tags
			for _, t := range tagsSlice {

				targetContainerPath := fmt.Sprintf("%v/%v:%v", r, *container, t)

				// tag container with additional tag
				log.Printf("Tagging container image %v\n", targetContainerPath)
				tagArgs := []string{
					"tag",
					sourceContainerPath,
					targetContainerPath,
				}
				runCommand("docker", tagArgs)

				loginIfRequired(credentials, targetContainerPath)

				log.Printf("Pushing container image %v\n", targetContainerPath)
				pushArgs := []string{
					"push",
					targetContainerPath,
				}
				runCommand("docker", pushArgs)
			}
		}

	default:
		log.Fatal("Set `command: <command>` on this step to build, push or tag")
	}
}

func validateRepositories(repositories string) {
	if repositories == "" {
		log.Fatal("Set `repositories:` to list at least one `- <repository>` (for example like `- extensions`)")
	}
}

func getCredentialsForContainer(credentials []*contracts.ContainerRepositoryCredentialConfig, containerImage string) *contracts.ContainerRepositoryCredentialConfig {
	if credentials != nil {
		for _, credentials := range credentials {
			containerImageSlice := strings.Split(containerImage, "/")
			containerRepo := strings.Join(containerImageSlice[:len(containerImageSlice)-1], "/")

			if containerRepo == credentials.Repository {
				return credentials
			}
		}
	}

	return nil
}

func loginIfRequired(credentials []*contracts.ContainerRepositoryCredentialConfig, containerImage string) {
	credential := getCredentialsForContainer(credentials, containerImage)
	if credential != nil {

		log.Printf("Logging in to repository %v for image %v\n", credential.Repository, containerImage)
		loginArgs := []string{
			"login",
			"--username",
			credential.Username,
			"--password",
			credential.Password,
		}

		repositorySlice := strings.Split(credential.Repository, "/")
		if len(repositorySlice) > 1 {
			server := repositorySlice[0]
			loginArgs = append(loginArgs, server)
		}

		err := exec.Command("docker", loginArgs...).Run()
		handleError(err)
	}
}

func handleError(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func runCommand(command string, args []string) {
	log.Printf("Running command '%v %v'...", command, strings.Join(args, " "))
	cmd := exec.Command(command, args...)
	cmd.Dir = "/estafette-work"
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	handleError(err)
}

func tidyBuildVersionAsTag(buildVersion string) string {
	// A tag name must be valid ASCII and may contain lowercase and uppercase letters, digits, underscores, periods and dashes.
	// A tag name may not start with a period or a dash and may contain a maximum of 128 characters.
	reg := regexp.MustCompile(`[^a-zA-Z0-9_.\-]+`)
	return reg.ReplaceAllString(buildVersion, "-")
}

func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
