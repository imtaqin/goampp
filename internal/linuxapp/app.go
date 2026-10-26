//go:build linux

// Package linuxapp provides the initial native Linux desktop experience.
// Packages and daemons are managed by the host distribution; GoAMPP never
// downloads Windows executables or changes system configuration silently.
package linuxapp

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

type serviceDefinition struct {
	Name     string
	Binaries []string
	Units    []string
}

type serviceState struct {
	Definition serviceDefinition
	Binary     string
	Manager    string
	Unit       string
	Running    bool
}

var serviceDefinitions = []serviceDefinition{
	{Name: "Apache HTTP Server", Binaries: []string{"apache2", "httpd"}, Units: []string{"apache2", "httpd"}},
	{Name: "Nginx", Binaries: []string{"nginx"}, Units: []string{"nginx"}},
	{Name: "Caddy", Binaries: []string{"caddy"}, Units: []string{"caddy"}},
	{Name: "PHP-FPM", Binaries: []string{"php-fpm", "php-fpm8.6", "php-fpm8.5", "php-fpm8.4", "php-fpm8.3", "php-fpm8.2", "php-fpm8.1", "php-fpm8.0"}, Units: []string{"php-fpm", "php8.6-fpm", "php8.5-fpm", "php8.4-fpm", "php8.3-fpm", "php8.2-fpm", "php8.1-fpm", "php8.0-fpm"}},
	{Name: "MariaDB / MySQL", Binaries: []string{"mariadbd", "mysqld"}, Units: []string{"mariadb", "mysql", "mysqld"}},
	{Name: "PostgreSQL", Binaries: []string{"postgres"}, Units: []string{"postgresql"}},
	{Name: "Redis", Binaries: []string{"redis-server"}, Units: []string{"redis-server", "redis"}},
	{Name: "Memcached", Binaries: []string{"memcached"}, Units: []string{"memcached"}},
	{Name: "RabbitMQ", Binaries: []string{"rabbitmq-server"}, Units: []string{"rabbitmq-server"}},
	{Name: "Docker Engine", Binaries: []string{"dockerd"}, Units: []string{"docker"}},
}

type toolDefinition struct {
	Name      string
	Binaries  []string
	Arguments []string
}

var toolDefinitions = []toolDefinition{
	{Name: "PHP", Binaries: []string{"php"}, Arguments: []string{"--version"}},
	{Name: "Composer", Binaries: []string{"composer"}, Arguments: []string{"--version"}},
	{Name: "Node.js", Binaries: []string{"node"}, Arguments: []string{"--version"}},
	{Name: "npm", Binaries: []string{"npm"}, Arguments: []string{"--version"}},
	{Name: "Python", Binaries: []string{"python3", "python"}, Arguments: []string{"--version"}},
	{Name: "Go", Binaries: []string{"go"}, Arguments: []string{"version"}},
	{Name: "Java", Binaries: []string{"java"}, Arguments: []string{"-version"}},
	{Name: "Rust", Binaries: []string{"rustc"}, Arguments: []string{"--version"}},
	{Name: "Cargo", Binaries: []string{"cargo"}, Arguments: []string{"--version"}},
	{Name: "Ruby", Binaries: []string{"ruby"}, Arguments: []string{"--version"}},
	{Name: "Bundler", Binaries: []string{"bundle"}, Arguments: []string{"--version"}},
	{Name: "Elixir", Binaries: []string{"elixir"}, Arguments: []string{"--version"}},
	{Name: "Erlang", Binaries: []string{"erl"}, Arguments: []string{"-noshell", "-eval", "io:format(\"~s~n\", [erlang:system_info(otp_release)]), halt()."}},
	{Name: "Maven", Binaries: []string{"mvn"}, Arguments: []string{"--version"}},
	{Name: "Gradle", Binaries: []string{"gradle"}, Arguments: []string{"--version"}},
	{Name: "CMake", Binaries: []string{"cmake"}, Arguments: []string{"--version"}},
	{Name: "GCC", Binaries: []string{"gcc"}, Arguments: []string{"--version"}},
	{Name: "Perl", Binaries: []string{"perl"}, Arguments: []string{"--version"}},
	{Name: "SQLite", Binaries: []string{"sqlite3"}, Arguments: []string{"--version"}},
	{Name: "Git", Binaries: []string{"git"}, Arguments: []string{"--version"}},
	{Name: "Docker CLI", Binaries: []string{"docker"}, Arguments: []string{"--version"}},
	{Name: "PostgreSQL client", Binaries: []string{"psql"}, Arguments: []string{"--version"}},
}

func Run() {
	a := app.NewWithID("cc.thio.goampp.linux")
	themePreference := a.Preferences().StringWithFallback("theme", "System")
	// Light mode is intentionally not exposed until its palette is complete.
	// Reset old saved Light preferences to System so existing installs recover cleanly.
	if themePreference != "Dark" && themePreference != "System" {
		themePreference = "System"
		a.Preferences().SetString("theme", themePreference)
	}
	a.Settings().SetTheme(newLinuxGoamppTheme(themePreference))
	if logo := linuxLogoResource(); logo != nil {
		a.SetIcon(logo)
	}
	w := a.NewWindow("GoAMPP for Linux")
	w.Resize(fyne.NewSize(1120, 760))
	w.CenterOnScreen()

	desktopApp, trayAvailable := a.(desktop.App)
	if trayAvailable {
		if logo := linuxLogoResource(); logo != nil {
			desktopApp.SetSystemTrayIcon(logo)
		}
		desktopApp.SetSystemTrayMenu(fyne.NewMenu("GoAMPP",
			fyne.NewMenuItem("Show GoAMPP", func() {
				fyne.Do(func() { w.Show() })
			}),
			fyne.NewMenuItem("Quit GoAMPP", func() {
				stopAllProjects()
				a.Quit()
			}),
		))
	}

	projectDir := projectDirectory()
	_ = os.MkdirAll(projectDir, 0o755)

	serviceList := container.NewVBox()
	serviceStatus := widget.NewLabel("Services are detected from your system PATH and service manager.")
	serviceStatus.Wrapping = fyne.TextWrapWord
	serviceSearch := widget.NewEntry()
	serviceSearch.SetPlaceHolder("Search services and runtimes")
	serviceSearch.ActionItem = widget.NewIcon(theme.NewDisabledResource(theme.SearchIcon()))
	var cachedServices []serviceState
	var refreshServices func()
	var refreshButton *widget.Button

	renderServiceList := func(states []serviceState) {
		serviceList.Objects = nil
		query := strings.ToLower(strings.TrimSpace(serviceSearch.Text))
		groups := []struct {
			title string
			match func(serviceState) bool
		}{
			{"WEB SERVERS", func(state serviceState) bool {
				return state.Definition.Name == "Apache HTTP Server" || state.Definition.Name == "Nginx" || state.Definition.Name == "Caddy"
			}},
			{"DATABASES & CACHES", func(state serviceState) bool {
				return state.Definition.Name == "MariaDB / MySQL" || state.Definition.Name == "PostgreSQL" ||
					state.Definition.Name == "Redis" || state.Definition.Name == "Memcached"
			}},
			{"QUEUES & CONTAINERS", func(state serviceState) bool {
				return state.Definition.Name == "RabbitMQ" || state.Definition.Name == "Docker Engine"
			}},
			{"LANGUAGES & RUNTIMES", func(state serviceState) bool { return state.Definition.Name == "PHP-FPM" }},
		}
		for _, group := range groups {
			var matched []serviceState
			for _, state := range states {
				if group.match(state) && (query == "" ||
					strings.Contains(strings.ToLower(state.Definition.Name+" "+state.Unit+" "+state.Binary), query)) {
					matched = append(matched, state)
				}
			}
			if len(matched) == 0 {
				continue
			}
			serviceList.Add(linuxSectionLabel(group.title))
			serviceList.Add(linuxVSpace(3))
			rows := container.NewVBox()
			for i, state := range matched {
				if i > 0 {
					rows.Add(linuxVSpace(5))
				}
				rows.Add(makeServiceCard(w, state, refreshServices))
			}
			serviceList.Add(rows)
			serviceList.Add(linuxVSpace(8))
		}
		var matchingTools []toolDefinition
		for _, tool := range toolDefinitions {
			if query == "" || strings.Contains(strings.ToLower(tool.Name+" "+strings.Join(tool.Binaries, " ")), query) {
				matchingTools = append(matchingTools, tool)
			}
		}
		if len(matchingTools) > 0 {
			serviceList.Add(linuxSectionLabel("LANGUAGES & DEVELOPER TOOLS"))
			serviceList.Add(linuxVSpace(3))
			rows := container.NewVBox()
			for i, tool := range matchingTools {
				if i > 0 {
					rows.Add(linuxVSpace(5))
				}
				rows.Add(makeToolCard(w, tool))
			}
			serviceList.Add(rows)
		}
		if len(serviceList.Objects) == 0 {
			serviceList.Add(container.NewCenter(widget.NewLabel("No matching services or runtimes.")))
		}
		serviceList.Refresh()
	}

	serviceSearch.OnChanged = func(string) { renderServiceList(cachedServices) }
	refreshServices = func() {
		linuxResetToolVersionCache()
		serviceStatus.SetText("Checking installed services...")
		linuxSetStatus("Refreshing installed services")
		if refreshButton != nil {
			refreshButton.Disable()
		}
		go func() {
			states := discoverServices()
			fyne.Do(func() {
				cachedServices = states
				renderServiceList(cachedServices)
				if len(states) > 0 && states[0].Manager == "" {
					serviceStatus.SetText(fmt.Sprintf("No supported service manager detected. %d service types can still be inspected, but service controls are unavailable.", len(states)))
				} else {
					serviceStatus.SetText(fmt.Sprintf("Showing %d service types and %d developer-tool entries. Installed runtimes are detected from PATH; GoAMPP never changes system packages without your action.", len(states), len(toolDefinitions)))
				}
				if refreshButton != nil {
					refreshButton.Enable()
				}
			})
		}()
	}
	refreshButton = widget.NewButtonWithIcon("Refresh", theme.ViewRefreshIcon(), refreshServices)
	serviceToolbar := container.NewBorder(nil, nil, serviceStatus, linuxMinSize(serviceSearch, 210, 0), linuxGap(1))
	servicePage := container.NewBorder(
		linuxPad(linuxSurface(serviceToolbar, 8, 8, 12, 12), 0, 8, 0, 0),
		nil, nil, nil,
		container.NewVScroll(linuxPad(serviceList, 4, 10, 2, 2)),
	)

	rootLabel := widget.NewLabel(projectDir)
	rootLabel.Wrapping = fyne.TextWrapBreak
	projectNameEntry := widget.NewEntry()
	projectNameEntry.SetPlaceHolder("my-first-project")
	templateSelect := widget.NewSelect(projectCreationOptions(), nil)
	templateDescription := widget.NewLabel("")
	updateTemplateDescription := func(name string) {
		templateDescription.SetText(projectCreationDescription(name))
	}
	templateSelect.OnChanged = updateTemplateDescription
	templateSelect.SetSelected("Static HTML")
	updateTemplateDescription(templateSelect.Selected)
	projectStatus := widget.NewLabel("Create a starter project. Existing folders are never overwritten.")
	projectList := container.NewVBox()
	var refreshProjects func()
	refreshProjects = func() {
		projectList.Objects = nil
		names := listProjects(projectDir)
		if len(names) == 0 {
			projectList.Add(container.NewCenter(container.NewVBox(
				container.NewCenter(widget.NewIcon(theme.FolderIcon())),
				container.NewCenter(widget.NewLabel("No projects yet")),
				container.NewCenter(widget.NewLabel("Create a starter above to get started.")),
			)))
		} else {
			for _, name := range names {
				projectName := name
				openButton := widget.NewButtonWithIcon("Open folder", theme.FolderOpenIcon(), func() {
					if err := openExternal(filepath.Join(projectDir, projectName)); err != nil {
						dialog.ShowError(err, w)
					}
				})
				actionButton := widget.NewButtonWithIcon("Run", theme.MediaPlayIcon(), func() {
					url, err := startProject(projectDir, projectName)
					if err != nil {
						dialog.ShowError(err, w)
						refreshProjects()
						return
					}
					projectStatus.SetText("Starting " + projectName + " at " + url + ". The preview will open when ready; output is saved in .goampp-run.log.")
					linuxAddLog("Starting project " + projectName + " at " + url)
					refreshProjects()
					openPreviewWhenReady(projectDir, projectName, url, func(runErr error) {
						fyne.Do(func() {
							if runErr != nil {
								projectStatus.SetText("Could not start " + projectName + ": " + runErr.Error() + ". Check .goampp-run.log.")
								linuxAddLog("Project " + projectName + " failed to start: " + runErr.Error())
							} else {
								projectStatus.SetText("Running " + projectName + " at " + url + ".")
								linuxAddLog("Project " + projectName + " is running at " + url)
							}
							refreshProjects()
						})
					})
				})
				if _, err := projectRunSpecForDir(filepath.Join(projectDir, projectName)); err != nil {
					actionButton.SetText("No runner")
					actionButton.SetIcon(theme.InfoIcon())
					actionButton.Disable()
				} else if isProjectRunning(projectDir, projectName) {
					actionButton.SetText("Stop")
					actionButton.SetIcon(theme.MediaStopIcon())
					actionButton.OnTapped = func() {
						actionButton.Disable()
						actionButton.SetText("Stopping…")
						go func() {
							err := stopProject(projectDir, projectName)
							fyne.Do(func() {
								if err != nil {
									projectStatus.SetText("Could not stop " + projectName + ": " + err.Error())
									linuxAddLog("Could not stop project " + projectName + ": " + err.Error())
									dialog.ShowError(err, w)
								} else {
									projectStatus.SetText("Stopped " + projectName + ".")
									linuxAddLog("Stopped project " + projectName)
								}
								refreshProjects()
							})
						}()
					}
				}
				actions := []fyne.CanvasObject{actionButton}
				logPath := filepath.Join(projectDir, projectName, ".goampp-run.log")
				logButton := widget.NewButtonWithIcon("Logs", theme.DocumentIcon(), func() {
					showProjectLog(w, projectName, logPath)
				})
				actions = append(actions, logButton, openButton)
				nameLabel := widget.NewLabelWithStyle(projectName, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
				pathLabel := widget.NewLabel(filepath.Join(projectDir, projectName))
				pathLabel.Wrapping = fyne.TextWrapBreak
				projectIdentity := container.NewHBox(linuxLetterMark(projectName, 38), linuxHGap(10), nameLabel)
				projectHeader := container.NewBorder(nil, nil, projectIdentity, container.NewHBox(actions...), linuxGap(1))
				projectRow := container.NewVBox(projectHeader, linuxPad(pathLabel, 4, 0, 48, 0))
				projectList.Add(linuxSurface(projectRow, 8, 8, 10, 10))
				projectList.Add(linuxGap(8))
			}
		}
		projectList.Refresh()
	}
	var createButton *widget.Button
	createButton = widget.NewButtonWithIcon("Create project", theme.ContentAddIcon(), func() {
		name := strings.TrimSpace(projectNameEntry.Text)
		templateName := templateSelect.Selected
		if !projectNamePattern.MatchString(name) {
			dialog.ShowInformation("Projects", "Use 1–48 letters, numbers, underscores, or hyphens. The first character must be a letter or number.", w)
			return
		}
		if templateName == "" {
			dialog.ShowInformation("Projects", "Choose a project template first.", w)
			return
		}
		createButton.Disable()
		projectNameEntry.Disable()
		templateSelect.Disable()
		projectStatus.SetText("Creating " + templateName + " in the background… Output is added to the activity log.")
		linuxSetStatus("Creating project: " + name)
		linuxAddLog("Creating project " + name + " using " + templateName)
		go func() {
			created, err := createProjectWithLog(projectDir, name, templateName, func(line string) {
				linuxAddLog("[" + name + "] " + line)
			})
			fyne.Do(func() {
				createButton.Enable()
				projectNameEntry.Enable()
				templateSelect.Enable()
				if err != nil {
					if created != "" {
						projectStatus.SetText("Project creation failed. Partial files were kept at " + created + ". Inspect Logs before retrying.")
						linuxAddLog("Project creation failed for " + name + ": " + err.Error())
						refreshProjects()
					} else {
						projectStatus.SetText("Could not create " + name + ": " + err.Error())
						linuxAddLog("Project creation failed for " + name + ": " + err.Error())
					}
					linuxSetStatus("Project creation failed: " + name)
					dialog.ShowError(err, w)
					return
				}
				projectStatus.SetText("Created " + created + ". See README.md or Logs for next steps.")
				linuxAddLog("Created project " + filepath.Base(created) + " using " + templateName)
				linuxSetStatus("Project created: " + filepath.Base(created))
				projectNameEntry.SetText("")
				refreshProjects()
				dialog.ShowInformation("Project created", "Your project files are ready. GoAMPP keeps framework creation output in the project's Logs file.", w)
			})
		}()
	})
	projectActions := container.NewHBox(
		widget.NewButtonWithIcon("Open folder", theme.FolderOpenIcon(), func() {
			if err := openExternal(projectDir); err != nil {
				dialog.ShowError(err, w)
			}
		}),
		widget.NewButtonWithIcon("Terminal", theme.ComputerIcon(), func() {
			shell := "bash"
			if _, err := exec.LookPath(shell); err != nil {
				shell = "sh"
			}
			if err := openLinuxTerminal(projectDir, shell, "-l"); err != nil {
				dialog.ShowError(err, w)
			}
		}),
		widget.NewButtonWithIcon("Open localhost", theme.ComputerIcon(), func() {
			if err := openExternal("http://localhost/"); err != nil {
				dialog.ShowError(err, w)
			}
		}),
		widget.NewButtonWithIcon("Refresh", theme.ViewRefreshIcon(), refreshProjects),
	)
	createCard := linuxSurface(container.NewVBox(
		container.NewVBox(
			widget.NewLabelWithStyle("New project", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			widget.NewLabel("Create a safe starter in your local GoAMPP workspace."),
		),
		linuxVSpace(8),
		container.New(layout.NewFormLayout(),
			widget.NewLabel("Name"), projectNameEntry,
			widget.NewLabel("Template"), templateSelect,
		),
		linuxVSpace(4),
		templateDescription,
		linuxVSpace(8),
		container.NewBorder(nil, nil, nil, createButton, projectStatus),
	), 10, 10, 12, 12)
	projectsCard := linuxSurface(container.NewVBox(
		widget.NewLabelWithStyle("Your projects", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		widget.NewLabel("Run a starter, inspect its log, or open its folder."),
		linuxVSpace(8),
		projectList,
	), 10, 10, 12, 12)
	installCard := linuxSurface(container.NewVBox(
		widget.NewLabelWithStyle("System packages", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		widget.NewLabel("GoAMPP uses packages managed by your distribution and never silently installs system services."),
		widget.NewButtonWithIcon("Show package command", theme.ComputerIcon(), func() {
			command := suggestedInstallCommand()
			entry := widget.NewEntry()
			entry.SetText(command)
			entry.MultiLine = true
			entry.Wrapping = fyne.TextWrapBreak
			copyButton := widget.NewButtonWithIcon("Copy command", theme.ContentCopyIcon(), func() {
				w.Clipboard().SetContent(entry.Text)
				linuxAddLog("Package command copied")
				dialog.ShowInformation("Copied", "Installation command copied to clipboard.", w)
			})
			dialog.ShowCustom("Native package command", "Close", container.NewVBox(entry, copyButton), w)
		}),
	), 10, 10, 12, 12)
	projectsPage := container.NewVScroll(linuxPad(container.NewVBox(
		createCard,
		linuxVSpace(8),
		projectsCard,
		linuxVSpace(8),
		installCard,
	), 0, 12, 0, 0))
	refreshProjects()

	themes := []string{"System", "Dark"}
	themeControl := newLinuxSegmented(themes, func(index int) {
		preference := themes[index]
		a.Preferences().SetString("theme", preference)
		a.Settings().SetTheme(newLinuxGoamppTheme(preference))
		refreshLinuxThemeSurfaces()
		linuxSetStatus("Theme set to " + preference)
		linuxAddLog("Appearance theme changed to " + preference)
	})
	for i, preference := range themes {
		if preference == themePreference {
			themeControl.setSelected(i, false)
			break
		}
	}
	lightModeInfo := linuxSurface(container.NewVBox(
		container.NewHBox(
			widget.NewIcon(theme.InfoIcon()),
			linuxBoldText("Light mode is not available yet", 12.5),
		),
		widget.NewLabel("Flessan, the maintainer working on this project, has not finished the light theme yet. Contributions are open on GitHub if you'd like to help build it."),
		widget.NewButtonWithIcon("Contribute on GitHub", theme.ComputerIcon(), func() {
			if err := openExternal("https://github.com/flessan/goampp-linux"); err != nil {
				dialog.ShowError(err, w)
			}
		}),
	), 8, 8, 12, 12)
	appearanceRow := container.NewVBox(
		container.New(layout.NewCustomPaddedLayout(8, 8, 12, 8), container.NewBorder(
			nil, nil,
			container.NewVBox(
				widget.NewLabelWithStyle("Theme", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
				widget.NewLabel("System follows your Linux desktop appearance. Dark is always available."),
			),
			container.NewCenter(themeControl.obj),
		)),
		lightModeInfo,
	)

	var trayPreference fyne.CanvasObject
	if trayAvailable {
		closeToTray := widget.NewCheck("Keep GoAMPP running in the system tray when the window closes", func(enabled bool) {
			a.Preferences().SetBool("close-to-tray", enabled)
			if enabled {
				linuxSetStatus("Closing the window will keep GoAMPP in the system tray")
			} else {
				linuxSetStatus("Closing the window will quit GoAMPP")
			}
		})
		closeToTray.SetChecked(a.Preferences().BoolWithFallback("close-to-tray", false))
		trayPreference = linuxSurface(container.NewVBox(
			linuxBoldText("Window behavior", 13),
			closeToTray,
			linuxMutedText("The system-tray menu can reopen or quit the application.", 11.5),
		), 8, 8, 12, 12)
	} else {
		trayPreference = linuxSurface(container.NewVBox(
			linuxBoldText("Window behavior", 13),
			widget.NewLabel("Your current Linux desktop driver does not expose a system-tray integration."),
		), 8, 8, 12, 12)
	}

	settingsPage := container.NewVScroll(linuxPad(container.NewVBox(
		linuxSectionLabel("BEHAVIOR"),
		trayPreference,
		linuxSectionLabel("APPEARANCE"),
		linuxSurface(appearanceRow, 2, 2, 2, 2),
		linuxSectionLabel("PATHS"),
		linuxSurface(container.NewVBox(
			widget.NewLabelWithStyle("Project workspace", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			rootLabel,
			widget.NewButtonWithIcon("Open workspace", theme.FolderOpenIcon(), func() {
				if err := openExternal(projectDir); err != nil {
					dialog.ShowError(err, w)
				}
			}),
		), 10, 10, 12, 12),
		linuxSectionLabel("TOOLS"),
		linuxSurface(container.NewVBox(
			widget.NewLabelWithStyle("System package manager", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			widget.NewLabel("Service installation and system changes remain explicit user actions."),
			container.NewHBox(
				widget.NewButtonWithIcon("Open terminal", theme.ComputerIcon(), func() {
					shell := "bash"
					if _, err := exec.LookPath(shell); err != nil {
						shell = "sh"
					}
					if err := openLinuxTerminal(projectDir, shell, "-l"); err != nil {
						dialog.ShowError(err, w)
					}
				}),
				widget.NewButtonWithIcon("PostgreSQL console", theme.ComputerIcon(), func() {
					if _, err := exec.LookPath("psql"); err != nil {
						showLinuxPackageCommand(w, "PostgreSQL client", true)
						return
					}
					if err := openLinuxTerminal(projectDir, "psql"); err != nil {
						dialog.ShowError(err, w)
					}
				}),
				widget.NewButtonWithIcon("Copy install command", theme.ContentCopyIcon(), func() {
					w.Clipboard().SetContent(suggestedInstallCommand())
					linuxAddLog("Suggested package command copied")
					dialog.ShowInformation("Copied", "Installation command copied to clipboard.", w)
				}),
			),
		), 10, 10, 12, 12),
		linuxSectionLabel("ABOUT"),
		linuxSurface(container.NewHBox(
			linuxImage(linuxLogoResource(), 40, "GoAMPP"),
			linuxHSpace(12),
			container.NewVBox(
				linuxBoldText("GoAMPP for Linux", 13),
				linuxMutedText("Native desktop panel for distribution-managed services.", 11.5),
				linuxMutedText("Early Linux mode · Windows feature parity is still in progress.", 10.5),
			),
		), 10, 10, 12, 12),
	), 8, 12, 14, 14))

	pages := []*linuxPage{
		{key: "services", title: "Services", icon: theme.ComputerIcon(), actions: refreshButton, body: servicePage},
		{key: "projects", title: "Projects", icon: theme.FolderIcon(), actions: projectActions, body: projectsPage},
		{key: "vhosts", title: "Virtual Hosts", icon: theme.HomeIcon(), body: buildLinuxVHostsPage(w, projectDir)},
		{key: "editor", title: "Config Editor", icon: theme.DocumentCreateIcon(), body: buildLinuxConfigEditorPage(w)},
		{key: "startup", title: "Startup", icon: theme.MediaPlayIcon(), body: buildLinuxStartupPage(w)},
		{key: "settings", title: "Settings", icon: theme.SettingsIcon(), body: settingsPage},
	}
	attachLinuxShell(a, w, pages, "services")
	linuxAddLog("GoAMPP for Linux started")
	refreshServices()
	w.SetCloseIntercept(func() {
		if trayAvailable && a.Preferences().BoolWithFallback("close-to-tray", false) {
			w.Hide()
			linuxSetStatus("GoAMPP is running in the system tray")
			linuxAddLog("Main window hidden to system tray")
			return
		}
		stopAllProjects()
		a.Quit()
	})
	w.ShowAndRun()
}

func makeServiceCard(w fyne.Window, state serviceState, refresh func()) fyne.CanvasObject {
	detail := "Not detected"
	if state.Unit != "" {
		detail = "service unit: " + state.Unit
		if state.Binary != "" {
			detail += "  •  " + state.Binary
		}
	} else if state.Binary != "" {
		detail = state.Binary + "  •  no matching service unit found"
	}

	status := "Not installed"
	switch {
	case state.Unit != "" && state.Running:
		status = "Running"
	case state.Unit != "":
		status = "Stopped"
	case state.Binary != "":
		status = "Installed"
	}

	statusAccent := theme.Color(linuxColorMuted)
	switch status {
	case "Running":
		statusAccent = theme.Color(theme.ColorNameSuccess)
	case "Installed":
		statusAccent = theme.Color(theme.ColorNamePrimary)
	case "Stopped":
		statusAccent = theme.Color(theme.ColorNameWarning)
	}
	detailLabel := widget.NewLabel(detail)
	detailLabel.Wrapping = fyne.TextWrapBreak
	titleLabel := widget.NewLabelWithStyle(state.Definition.Name, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	heading := container.NewHBox(
		linuxImage(linuxServiceResource(state.Definition.Name), 38, state.Definition.Name),
		linuxHGap(10),
		container.NewVBox(titleLabel, linuxStatusBadge(status, statusAccent)),
	)
	var action *widget.Button
	action = widget.NewButtonWithIcon("Start", theme.MediaPlayIcon(), func() {
		runServiceAction(w, action, state.Manager, state.Unit, "start", refresh)
	})
	action.Importance = widget.MediumImportance
	if state.Running {
		action.SetText("Stop")
		action.SetIcon(theme.MediaStopIcon())
		action.OnTapped = func() {
			runServiceAction(w, action, state.Manager, state.Unit, "stop", refresh)
		}
	}
	if state.Unit == "" {
		if state.Binary == "" {
			action.SetText("Install guide")
			action.SetIcon(theme.DownloadIcon())
			action.OnTapped = func() { showLinuxPackageCommand(w, state.Definition.Name, false) }
			action.Enable()
		} else {
			action.SetText("No service unit")
			action.SetIcon(theme.InfoIcon())
			action.Disable()
		}
	}
	moreButton := widget.NewButtonWithIcon("More", theme.MoreHorizontalIcon(), func() {
		showLinuxServiceActions(w, state, refresh)
	})
	header := container.NewBorder(nil, nil, heading, container.NewHBox(action, moreButton), linuxGap(1))
	details := linuxPad(detailLabel, 0, 0, 48, 0)
	return linuxSurface(container.NewVBox(header, details), 10, 10, 12, 12)
}


const maxProjectLogPreviewBytes int64 = 1024 * 1024

func readProjectLogTail(path string, limit int64) (string, bool, error) {
	if limit <= 0 {
		return "", false, fmt.Errorf("log preview limit must be positive")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", false, err
	}
	start := int64(0)
	truncated := info.Size() > limit
	if truncated {
		start = info.Size() - limit
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return "", false, err
	}
	data, err := io.ReadAll(io.LimitReader(file, limit))
	if err != nil {
		return "", false, err
	}
	return string(data), truncated, nil
}

func showProjectLog(w fyne.Window, projectName, logPath string) {
	logText := widget.NewLabel("")
	logText.Wrapping = fyne.TextWrapBreak
	logText.TextStyle = fyne.TextStyle{Monospace: true}

	refreshLog := func() {
		data, truncated, err := readProjectLogTail(logPath, maxProjectLogPreviewBytes)
		if err != nil {
			if os.IsNotExist(err) {
				logText.SetText("No project log exists yet. Run this project first, then select Refresh.")
				return
			}
			logText.SetText("Could not read project log: " + err.Error())
			return
		}
		if data == "" {
			logText.SetText("The project log is empty.")
			return
		}
		if truncated {
			data = "[Showing the last 1 MiB of this log file. Open externally to inspect the full log.]\n\n" + data
		}
		logText.SetText(strings.TrimSpace(data))
	}
	refreshLog()

	refreshButton := widget.NewButtonWithIcon("Refresh", theme.ViewRefreshIcon(), refreshLog)
	openButton := widget.NewButtonWithIcon("Open externally", theme.FolderOpenIcon(), func() {
		if err := openExternal(logPath); err != nil {
			dialog.ShowError(err, w)
		}
	})
	content := container.NewBorder(
		container.NewHBox(refreshButton, openButton),
		nil, nil, nil,
		container.NewScroll(logText),
	)
	d := dialog.NewCustom("Project logs · "+projectName, "Close", content, w)
	d.Resize(fyne.NewSize(820, 520))
	d.Show()
}

func runServiceAction(w fyne.Window, action *widget.Button, manager, unit, verb string, refresh func()) {
	action.Disable()
	linuxSetStatus(strings.Title(verb) + " service " + unit)
	linuxAddLog(strings.Title(verb) + " service " + unit)
	switch verb {
	case "start":
		action.SetText("Starting…")
	case "stop":
		action.SetText("Stopping…")
	case "restart":
		action.SetText("Restarting…")
	}
	go func() {
		err := controlUnit(manager, unit, verb)
		fyne.Do(func() {
			action.Enable()
			if err != nil {
				switch verb {
				case "start":
					action.SetText("Start")
					action.SetIcon(theme.MediaPlayIcon())
				case "stop":
					action.SetText("Stop")
					action.SetIcon(theme.MediaStopIcon())
				case "restart":
					action.SetText("Restart")
					action.SetIcon(theme.MediaReplayIcon())
				}
				linuxAddLog("Service " + unit + " " + verb + " failed: " + err.Error())
				linuxSetStatus("Service action failed: " + unit)
				dialog.ShowError(err, w)
			} else {
				switch verb {
				case "start":
					action.SetText("Stop")
					action.SetIcon(theme.MediaStopIcon())
				case "stop":
					action.SetText("Start")
					action.SetIcon(theme.MediaPlayIcon())
				case "restart":
					action.SetText("Restart")
					action.SetIcon(theme.MediaReplayIcon())
				}
				linuxAddLog("Service " + unit + " " + verb + " completed")
				linuxSetStatus("Service " + unit + " " + verb + " completed")
			}
			refresh()
		})
	}()
}

func makeToolCard(w fyne.Window, tool toolDefinition) fyne.CanvasObject {
	binary := findExecutable(tool.Binaries)
	status := "Not installed"
	if binary != "" {
		status = "Installed at " + binary
	}
	statusLabel := widget.NewLabel(status)
	statusLabel.Wrapping = fyne.TextWrapBreak
	if binary != "" && len(tool.Arguments) > 0 {
		if version, ok := linuxCachedToolVersion(tool.Name); ok {
			statusLabel.SetText(binary + " · " + version)
		} else {
			linuxRequestToolVersion(tool.Name, binary, append([]string(nil), tool.Arguments...), statusLabel)
		}
	}
	title := widget.NewLabelWithStyle(tool.Name, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	identity := container.NewHBox(
		linuxImage(linuxToolResource(tool.Name), 36, tool.Name),
		linuxHGap(10),
		container.NewCenter(title),
	)
	if binary == "" {
		installButton := widget.NewButtonWithIcon("Install guide", theme.DownloadIcon(), func() {
			showLinuxPackageCommand(w, tool.Name, true)
		})
		installButton.Importance = widget.LowImportance
		identity = container.NewBorder(nil, nil, identity, installButton, linuxGap(1))
	}
	return linuxSurface(container.NewVBox(
		identity,
		linuxPad(statusLabel, 4, 0, 46, 0),
	), 9, 9, 10, 10)
}

func runCommandWithTimeout(timeout time.Duration, command string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return exec.CommandContext(ctx, command, args...).CombinedOutput()
}

func discoverServices() []serviceState {
	manager := detectServiceManager()
	units := listSystemdUnits()
	initScripts := listInitScripts()
	states := make([]serviceState, 0, len(serviceDefinitions))
	for _, def := range serviceDefinitions {
		state := serviceState{Definition: def, Binary: findExecutable(def.Binaries), Manager: manager}
		for _, candidate := range def.Units {
			name := strings.TrimSuffix(candidate, ".service")
			if manager == "systemd" {
				if _, ok := units[name+".service"]; ok {
					state.Unit = name
					break
				}
			} else if manager == "openrc" || manager == "sysvinit" {
				if _, ok := initScripts[name]; ok {
					state.Unit = name
					break
				}
			}
		}
		if def.Name == "PHP-FPM" && state.Unit == "" {
			state.Unit = findPHPFPMUnit(manager, units, initScripts)
		}
		if state.Unit != "" {
			state.Running = serviceIsRunning(manager, state.Unit)
		}
		states = append(states, state)
	}
	return states
}

func findPHPFPMUnit(manager string, systemdUnits, initScripts map[string]struct{}) string {
	var candidates []string
	switch manager {
	case "systemd":
		for unit := range systemdUnits {
			if strings.HasPrefix(unit, "php") && strings.HasSuffix(unit, "-fpm.service") {
				candidates = append(candidates, strings.TrimSuffix(unit, ".service"))
			}
		}
	case "openrc", "sysvinit":
		for unit := range initScripts {
			if strings.HasPrefix(unit, "php") && strings.HasSuffix(unit, "-fpm") {
				candidates = append(candidates, unit)
			}
		}
	default:
		return ""
	}
	sort.Strings(candidates)
	if len(candidates) == 0 {
		return ""
	}
	return candidates[len(candidates)-1]
}

func detectServiceManager() string {
	_, systemctlErr := exec.LookPath("systemctl")
	_, rcServiceErr := exec.LookPath("rc-service")
	_, serviceErr := exec.LookPath("service")
	_, systemdErr := os.Stat("/run/systemd/system")
	_, initScriptsErr := os.Stat("/etc/init.d")
	return chooseServiceManager(systemctlErr == nil && systemdErr == nil, rcServiceErr == nil && initScriptsErr == nil, serviceErr == nil && initScriptsErr == nil)
}

func chooseServiceManager(systemdActive, openRCAvailable, sysVAvailable bool) string {
	switch {
	case systemdActive:
		return "systemd"
	case openRCAvailable:
		return "openrc"
	case sysVAvailable:
		return "sysvinit"
	default:
		return ""
	}
}

func listSystemdUnits() map[string]struct{} {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return map[string]struct{}{}
	}
	out, err := runCommandWithTimeout(5*time.Second, "systemctl", "list-unit-files", "--type=service", "--no-legend", "--no-pager")
	if err != nil {
		return map[string]struct{}{}
	}
	return parseSystemdUnitFiles(string(out))
}

func parseSystemdUnitFiles(output string) map[string]struct{} {
	units := make(map[string]struct{})
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && strings.HasSuffix(fields[0], ".service") {
			units[fields[0]] = struct{}{}
		}
	}
	return units
}

func listInitScripts() map[string]struct{} {
	units := make(map[string]struct{})
	entries, err := os.ReadDir("/etc/init.d")
	if err != nil {
		return units
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			units[entry.Name()] = struct{}{}
		}
	}
	return units
}

func serviceIsRunning(manager, unit string) bool {
	command, args, err := serviceCommand(manager, unit, "status")
	if err != nil {
		return false
	}
	_, err = runCommandWithTimeout(2*time.Second, command, args...)
	return err == nil
}

func validLinuxServiceUnit(unit string) bool {
	if unit == "" {
		return false
	}
	for _, r := range unit {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || strings.ContainsRune("_.@:-", r) {
			continue
		}
		return false
	}
	return true
}

func serviceCommand(manager, unit, action string) (string, []string, error) {
	if !validLinuxServiceUnit(unit) {
		return "", nil, fmt.Errorf("service unit is empty or contains unsupported characters")
	}
	switch action {
	case "start", "stop", "restart", "status":
	default:
		return "", nil, fmt.Errorf("unsupported service action %q", action)
	}
	switch manager {
	case "systemd":
		return "systemctl", []string{action, unit}, nil
	case "openrc":
		return "rc-service", []string{unit, action}, nil
	case "sysvinit":
		return "service", []string{unit, action}, nil
	default:
		return "", nil, fmt.Errorf("no supported service manager was detected")
	}
}

func controlUnit(manager, unit, action string) error {
	command, args, err := serviceCommand(manager, unit, action)
	if err != nil {
		return err
	}
	if _, err := exec.LookPath(command); err != nil {
		return fmt.Errorf("%s is not available; install or use your distribution's service manager", command)
	}
	output, err := runCommandWithTimeout(30*time.Second, command, args...)
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("%s %s %s failed: %s. Your account may need authorization to control this system service", command, action, unit, message)
	}
	return nil
}

func findExecutable(candidates []string) string {
	for _, candidate := range candidates {
		if path, err := exec.LookPath(candidate); err == nil {
			return path
		}
	}
	return ""
}

func projectDirectory() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".", "GoAMPP", "www")
	}
	return filepath.Join(home, "GoAMPP", "www")
}

func desktopOpenCommand(target string, lookup func(string) (string, error)) (string, []string, error) {
	candidates := []struct {
		name string
		args []string
	}{
		{name: "xdg-open"},
		{name: "gio", args: []string{"open"}},
		{name: "kde-open5"},
		{name: "kde-open"},
	}
	for _, candidate := range candidates {
		opener, err := lookup(candidate.name)
		if err != nil {
			continue
		}
		args := append(append([]string(nil), candidate.args...), target)
		return opener, args, nil
	}
	return "", nil, fmt.Errorf("no supported desktop opener was found; install xdg-utils or open manually: %s", target)
}

func openExternal(target string) error {
	opener, args, err := desktopOpenCommand(target, exec.LookPath)
	if err != nil {
		return err
	}
	cmd := exec.Command(opener, args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("open %s: %w", target, err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func suggestedInstallCommand() string {
	if _, err := exec.LookPath("pacman"); err == nil {
		return "sudo pacman -S --needed apache nginx php-fpm mariadb postgresql redis nodejs npm python python-pip go jdk-openjdk composer"
	}
	if _, err := exec.LookPath("dnf"); err == nil {
		return "sudo dnf install httpd nginx php-fpm mariadb-server postgresql-server redis nodejs npm python3 golang java-latest-openjdk composer"
	}
	if _, err := exec.LookPath("zypper"); err == nil {
		return "sudo zypper install apache2 nginx php-fpm mariadb postgresql-server redis nodejs npm python3 go java-17-openjdk-devel composer"
	}
	if _, err := exec.LookPath("apt-get"); err == nil {
		return "sudo apt install apache2 nginx php-fpm mariadb-server postgresql redis-server php-cli composer nodejs npm python3 python3-pip golang-go default-jdk"
	}
	return "# Install Apache/Nginx, PHP-FPM, MariaDB/PostgreSQL, Redis and your preferred runtimes using your Linux distribution's package manager."
}
