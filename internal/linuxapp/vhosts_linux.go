//go:build linux

package linuxapp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

type linuxVHost struct {
	Domain       string `json:"domain"`
	DocumentRoot string `json:"document_root"`
	Port         int    `json:"port"`
	Server       string `json:"server"`
	Enabled      bool   `json:"enabled"`
}

func linuxVHostsPath() string {
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "goampp", "vhosts.json")
}

func loadLinuxVHosts() ([]linuxVHost, error) {
	data, err := os.ReadFile(linuxVHostsPath())
	if os.IsNotExist(err) {
		return []linuxVHost{}, nil
	}
	if err != nil {
		return nil, err
	}
	var hosts []linuxVHost
	if err := json.Unmarshal(data, &hosts); err != nil {
		return nil, fmt.Errorf("read virtual host file: %w", err)
	}
	if hosts == nil {
		hosts = []linuxVHost{}
	}
	return hosts, nil
}

func saveLinuxVHosts(hosts []linuxVHost) error {
	path := linuxVHostsPath()
	data, err := json.MarshalIndent(hosts, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteLinuxFile(path, append(data, '\n'), 0o600)
}

func validLinuxHostDomain(domain string) bool {
	domain = strings.TrimSpace(strings.TrimSuffix(strings.ToLower(domain), "."))
	if len(domain) < 3 || len(domain) > 253 || !strings.Contains(domain, ".") {
		return false
	}
	for _, label := range strings.Split(domain, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '-' {
				return false
			}
		}
	}
	return true
}

func validLinuxDocumentRootPath(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	for _, r := range path {
		// Control characters can inject additional server directives. Dollar
		// signs trigger server-variable expansion in Apache and Nginx.
		if r < 0x20 || r == 0x7f || r == 36 {
			return false
		}
	}
	return true
}

func linuxHostConfig(host linuxVHost) string {
	root := strings.ReplaceAll(host.DocumentRoot, "\\", "\\\\")
	root = strings.ReplaceAll(root, "\"", "\\\"")
	port := host.Port
	if port < 1 || port > 65535 {
		port = 80
	}
	var snippets []string
	if host.Server == "apache" || host.Server == "both" {
		snippets = append(snippets, fmt.Sprintf("# Apache virtual host for %s\n<VirtualHost *:%d>\n    ServerName %s\n    DocumentRoot \"%s\"\n    <Directory \"%s\">\n        AllowOverride All\n        Require all granted\n    </Directory>\n    ErrorLog ${APACHE_LOG_DIR}/%s-error.log\n    CustomLog ${APACHE_LOG_DIR}/%s-access.log combined\n</VirtualHost>", host.Domain, port, host.Domain, root, root, host.Domain, host.Domain))
	}
	if host.Server == "nginx" || host.Server == "both" {
		nginxRoot := strings.ReplaceAll(host.DocumentRoot, "\\", "/")
		nginxRoot = strings.ReplaceAll(nginxRoot, "'", "\\'")
		snippets = append(snippets, fmt.Sprintf("# Nginx server block for %s\nserver {\n    listen %d;\n    server_name %s;\n    root '%s';\n    index index.php index.html index.htm;\n\n    location / {\n        try_files $uri $uri/ =404;\n    }\n}", host.Domain, port, host.Domain, nginxRoot))
	}
	return strings.Join(snippets, "\n\n")
}

func buildLinuxVHostsPage(w fyne.Window, projectDir string) fyne.CanvasObject {
	hosts, loadErr := loadLinuxVHosts()
	var selected = -1
	list := widget.NewList(
		func() int { return len(hosts) },
		func() fyne.CanvasObject {
			return container.NewVBox(widget.NewLabel("domain.test"), widget.NewLabel("/path/to/document/root"))
		},
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			row := obj.(*fyne.Container)
			if id < 0 || id >= len(hosts) {
				row.Objects[0].(*widget.Label).SetText("")
				row.Objects[1].(*widget.Label).SetText("")
				return
			}
			host := hosts[id]
			name := host.Domain
			if !host.Enabled {
				name += "  ·  Disabled"
			}
			row.Objects[0].(*widget.Label).SetText(name)
			row.Objects[0].(*widget.Label).TextStyle = fyne.TextStyle{Bold: true}
			row.Objects[1].(*widget.Label).SetText(host.DocumentRoot)
			row.Objects[1].(*widget.Label).Wrapping = fyne.TextWrapBreak
		},
	)
	domain := widget.NewEntry()
	domain.SetPlaceHolder("myapp.test")
	docroot := widget.NewEntry()
	docroot.SetPlaceHolder(filepath.Join(projectDir, "myapp", "public"))
	port := widget.NewEntry()
	port.SetText("80")
	server := widget.NewSelect([]string{"Apache", "Nginx", "Apache + Nginx"}, nil)
	server.SetSelected("Apache")
	status := widget.NewLabel("Virtual hosts are stored in your user config. GoAMPP will not edit /etc/hosts or system server config without your explicit action.")
	status.Wrapping = fyne.TextWrapWord
	enabled := true

	clear := func() {
		selected = -1
		domain.SetText("")
		docroot.SetText(projectDir)
		port.SetText("80")
		server.SetSelected("Apache")
		enabled = true
		list.UnselectAll()
	}
	loadSelected := func(index int) {
		if index < 0 || index >= len(hosts) {
			return
		}
		selected = index
		host := hosts[index]
		domain.SetText(host.Domain)
		docroot.SetText(host.DocumentRoot)
		port.SetText(strconv.Itoa(host.Port))
		switch host.Server {
		case "nginx":
			server.SetSelected("Nginx")
		case "both":
			server.SetSelected("Apache + Nginx")
		default:
			server.SetSelected("Apache")
		}
		enabled = host.Enabled
	}
	list.OnSelected = func(id widget.ListItemID) { loadSelected(int(id)) }

	save := widget.NewButtonWithIcon("Save host", theme.DocumentSaveIcon(), func() {
		name := strings.ToLower(strings.TrimSpace(domain.Text))
		root := strings.TrimSpace(docroot.Text)
		if !validLinuxDocumentRootPath(root) {
			dialog.ShowError(fmt.Errorf("document root must not contain control characters or dollar signs"), w)
			return
		}
		portNumber, err := strconv.Atoi(strings.TrimSpace(port.Text))
		if !validLinuxHostDomain(name) {
			dialog.ShowError(fmt.Errorf("enter a valid hostname such as myapp.test or myapp.localhost"), w)
			return
		}
		if portNumber < 1 || portNumber > 65535 {
			dialog.ShowError(fmt.Errorf("port must be a number from 1 to 65535"), w)
			return
		}
		if root == "" {
			dialog.ShowError(fmt.Errorf("document root is required"), w)
			return
		}
		root, err = filepath.Abs(root)
		if err != nil {
			dialog.ShowError(err, w)
			return
		}
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			dialog.ShowError(fmt.Errorf("document root must be an existing directory"), w)
			return
		}
		serverValue := "apache"
		switch server.Selected {
		case "Nginx":
			serverValue = "nginx"
		case "Apache + Nginx":
			serverValue = "both"
		}
		for i, current := range hosts {
			if i != selected && strings.EqualFold(current.Domain, name) {
				dialog.ShowError(fmt.Errorf("a virtual host named %s already exists", name), w)
				return
			}
		}
		host := linuxVHost{Domain: name, DocumentRoot: root, Port: portNumber, Server: serverValue, Enabled: enabled}
		if selected >= 0 && selected < len(hosts) {
			hosts[selected] = host
		} else {
			hosts = append(hosts, host)
			selected = len(hosts) - 1
		}
		if err := saveLinuxVHosts(hosts); err != nil {
			dialog.ShowError(err, w)
			return
		}
		list.Refresh()
		list.Select(selected)
		status.SetText("Saved " + name + " to " + linuxVHostsPath() + ". Copy its server snippet below and enable the matching web-server config yourself.")
		linuxAddLog("Virtual host saved: " + name)
	})
	newHost := widget.NewButtonWithIcon("New", theme.ContentAddIcon(), clear)
	toggle := widget.NewButton("Enable / disable", func() {
		if selected < 0 || selected >= len(hosts) {
			dialog.ShowInformation("Select a host", "Select a virtual host before changing its state.", w)
			return
		}
		hosts[selected].Enabled = !hosts[selected].Enabled
		if err := saveLinuxVHosts(hosts); err != nil {
			hosts[selected].Enabled = !hosts[selected].Enabled
			dialog.ShowError(err, w)
			return
		}
		enabled = hosts[selected].Enabled
		list.Refresh()
		if enabled {
			status.SetText("Host enabled in GoAMPP's local registry. The web-server snippet still needs to be applied to your system configuration.")
		} else {
			status.SetText("Host disabled in GoAMPP's local registry. Existing server config files are not changed automatically.")
		}
	})
	remove := widget.NewButtonWithIcon("Delete", theme.DeleteIcon(), func() {
		deleteIndex := selected
		if deleteIndex < 0 || deleteIndex >= len(hosts) {
			return
		}
		name := hosts[deleteIndex].Domain
		dialog.ShowConfirm("Delete virtual host", "Remove "+name+" from GoAMPP? Project files and system configuration are kept.", func(ok bool) {
			if !ok {
				return
			}
			if deleteIndex < 0 || deleteIndex >= len(hosts) || hosts[deleteIndex].Domain != name {
				dialog.ShowInformation("Virtual host changed", "The selected host list changed while the confirmation was open. Select the host again and retry.", w)
				return
			}
			hosts = append(hosts[:deleteIndex], hosts[deleteIndex+1:]...)
			if err := saveLinuxVHosts(hosts); err != nil {
				dialog.ShowError(err, w)
				return
			}
			linuxAddLog("Virtual host deleted: " + name)
			clear()
			list.Refresh()
		}, w)
	})
	browse := widget.NewButtonWithIcon("Browse", theme.FolderOpenIcon(), func() {
		d := dialog.NewFolderOpen(func(uri fyne.ListableURI, err error) {
			if err == nil && uri != nil {
				docroot.SetText(uri.Path())
			}
		}, w)
		d.Show()
	})
	copySnippet := widget.NewButtonWithIcon("Copy server config", theme.ContentCopyIcon(), func() {
		var host linuxVHost
		if selected >= 0 && selected < len(hosts) {
			host = hosts[selected]
		} else {
			name := strings.ToLower(strings.TrimSpace(domain.Text))
			root, _ := filepath.Abs(strings.TrimSpace(docroot.Text))
			p, _ := strconv.Atoi(port.Text)
			serverValue := "apache"
			if server.Selected == "Nginx" { serverValue = "nginx" } else if server.Selected == "Apache + Nginx" { serverValue = "both" }
			host = linuxVHost{Domain: name, DocumentRoot: root, Port: p, Server: serverValue, Enabled: enabled}
		}
		if !validLinuxHostDomain(host.Domain) || !validLinuxDocumentRootPath(host.DocumentRoot) {
			dialog.ShowInformation("Virtual host", "Fill in a valid domain and a document root without control characters or dollar signs first.", w)
			return
		}
		w.Clipboard().SetContent(linuxHostConfig(host))
		dialog.ShowInformation("Copied", "Generated server configuration copied to clipboard. Review it before adding it to Apache or Nginx.", w)
		linuxAddLog("Virtual host config copied: " + host.Domain)
	})
	copyHosts := widget.NewButtonWithIcon("Copy /etc/hosts entry", theme.ContentCopyIcon(), func() {
		name := strings.ToLower(strings.TrimSpace(domain.Text))
		if !validLinuxHostDomain(name) {
			dialog.ShowInformation("Virtual host", "Enter a valid hostname first.", w)
			return
		}
		w.Clipboard().SetContent("127.0.0.1 " + name)
		dialog.ShowInformation("Copied", "Add the copied mapping to /etc/hosts only if your local hostname needs it. This action does not modify the hosts file.", w)
	})
	form := container.New(layout.NewFormLayout(),
		widget.NewLabel("Domain"), domain,
		widget.NewLabel("Document root"), container.NewBorder(nil, nil, nil, browse, docroot),
		widget.NewLabel("Port"), port,
		widget.NewLabel("Web server"), server,
	)
	formCard := linuxSurface(container.NewVBox(
		linuxBoldText("Virtual host", 14),
		linuxMutedText("Manage host definitions and generate Apache/Nginx snippets.", 11.5),
		linuxVSpace(8),
		form,
		linuxVSpace(8),
		container.NewHBox(save, newHost, toggle, remove),
		linuxVSpace(4),
		container.NewHBox(copySnippet, copyHosts),
		linuxVSpace(6),
		status,
	), 10, 10, 12, 12)
	listCard := linuxSurface(container.NewBorder(
		linuxPad(linuxBoldText("Saved hosts", 14), 0, 8, 0, 0),
		nil, nil, nil, list,
	), 8, 8, 10, 10)
	if loadErr != nil {
		status.SetText("Could not load saved hosts: " + loadErr.Error())
	}
	if len(hosts) > 0 {
		loadSelected(0)
		list.Select(0)
	} else {
		docroot.SetText(projectDir)
	}
	return container.NewVScroll(linuxPad(container.NewVBox(
		formCard,
		linuxVSpace(8),
		listCard,
	), 0, 10, 0, 0))
}
