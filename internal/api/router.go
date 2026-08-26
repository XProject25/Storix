package api

import (
	"net/http"
	"strings"

	"github.com/XProject25/Storix/internal/store"
)

// Handler builds the complete HTTP handler: the JSON API under /api/v1 and
// the embedded web application on every other path.
func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()

	// ---- public, no session required ---------------------------------------
	mux.HandleFunc("GET /api/v1/system/status", a.handleSystemStatus)
	mux.HandleFunc("POST /api/v1/setup", a.handleSetup)
	mux.HandleFunc("POST /api/v1/auth/login", a.handleLogin)
	mux.HandleFunc("POST /api/v1/auth/logout", a.handleLogout)

	// ---- session ------------------------------------------------------------
	mux.HandleFunc("GET /api/v1/auth/me", a.requireAuth(a.handleMe))
	mux.HandleFunc("POST /api/v1/auth/password", a.requireAuth(a.handleChangePassword))
	mux.HandleFunc("POST /api/v1/auth/totp/setup", a.requireAuth(a.handleTOTPSetup))
	mux.HandleFunc("POST /api/v1/auth/totp/enable", a.requireAuth(a.handleTOTPEnable))
	mux.HandleFunc("POST /api/v1/auth/totp/disable", a.requireAuth(a.handleTOTPDisable))
	mux.HandleFunc("GET /api/v1/auth/sessions", a.requireAuth(a.handleListSessions))
	mux.HandleFunc("DELETE /api/v1/auth/sessions/{id}", a.requireAuth(a.handleRevokeSession))
	mux.HandleFunc("POST /api/v1/auth/preferences", a.requireAuth(a.handlePreferences))

	// ---- browsing -----------------------------------------------------------
	mux.HandleFunc("GET /api/v1/fs/list", a.requirePerm(store.PermView, a.handleList))
	mux.HandleFunc("GET /api/v1/fs/stat", a.requirePerm(store.PermView, a.handleStat))
	mux.HandleFunc("GET /api/v1/fs/search", a.requirePerm(store.PermView, a.handleSearch))
	mux.HandleFunc("GET /api/v1/fs/du", a.requirePerm(store.PermView, a.handleDu))
	mux.HandleFunc("GET /api/v1/fs/disk", a.requirePerm(store.PermView, a.handleDisk))
	mux.HandleFunc("GET /api/v1/fs/tree", a.requirePerm(store.PermView, a.handleTree))
	mux.HandleFunc("GET /api/v1/fs/usage", a.requirePerm(store.PermView, a.handleUsage))

	// ---- bulk rename and quotas ---------------------------------------------
	mux.HandleFunc("POST /api/v1/fs/rename-bulk/preview", a.requirePerm(store.PermRename, a.handleRenameBulkPreview))
	mux.HandleFunc("POST /api/v1/fs/rename-bulk", a.requirePerm(store.PermRename, a.handleRenameBulk))
	mux.HandleFunc("GET /api/v1/auth/quota", a.requireAuth(a.handleMyQuota))
	mux.HandleFunc("GET /api/v1/fs/duplicates", a.requirePerm(store.PermView, a.handleDuplicates))

	// ---- programmatic access ------------------------------------------------
	mux.HandleFunc("GET /api/v1/auth/tokens", a.requireAuth(a.handleListTokens))
	mux.HandleFunc("POST /api/v1/auth/tokens", a.requireAuth(a.handleCreateToken))
	mux.HandleFunc("DELETE /api/v1/auth/tokens/{id}", a.requireAuth(a.handleDeleteToken))
	mux.HandleFunc("GET /api/v1/users/{id}/quota", a.requirePerm(store.PermUsers, a.handleUserQuota))

	// ---- reading content ----------------------------------------------------
	mux.HandleFunc("GET /api/v1/fs/download", a.requirePerm(store.PermDownload, a.handleDownload))
	mux.HandleFunc("GET /api/v1/fs/download-zip", a.requirePerm(store.PermDownload, a.handleDownloadZip))
	mux.HandleFunc("GET /api/v1/fs/raw", a.requirePerm(store.PermView, a.handleRaw))
	mux.HandleFunc("GET /api/v1/fs/thumb", a.requirePerm(store.PermView, a.handleThumb))
	mux.HandleFunc("GET /api/v1/fs/text", a.requirePerm(store.PermView, a.handleReadText))
	mux.HandleFunc("GET /api/v1/fs/archive", a.requirePerm(store.PermView, a.handleArchivePreview))

	// ---- writing ------------------------------------------------------------
	mux.HandleFunc("PUT /api/v1/fs/text", a.requirePerm(store.PermEdit, a.handleWriteText))
	mux.HandleFunc("POST /api/v1/fs/mkdir", a.requirePerm(store.PermCreate, a.handleMkdir))
	mux.HandleFunc("POST /api/v1/fs/touch", a.requirePerm(store.PermCreate, a.handleTouch))
	mux.HandleFunc("POST /api/v1/fs/rename", a.requirePerm(store.PermRename, a.handleRename))
	mux.HandleFunc("POST /api/v1/fs/move", a.requirePerm(store.PermMove, a.handleMove))
	mux.HandleFunc("POST /api/v1/fs/copy", a.requirePerm(store.PermCopy, a.handleCopy))
	mux.HandleFunc("POST /api/v1/fs/delete", a.requirePerm(store.PermDelete, a.handleDelete))
	mux.HandleFunc("POST /api/v1/fs/compress", a.requirePerm(store.PermArchive, a.handleCompress))
	mux.HandleFunc("POST /api/v1/fs/extract", a.requirePerm(store.PermArchive, a.handleExtract))
	mux.HandleFunc("POST /api/v1/fs/chmod", a.requirePerm(store.PermAdvanced, a.handleChmod))
	mux.HandleFunc("POST /api/v1/fs/chown", a.requirePerm(store.PermAdvanced, a.handleChown))

	// ---- recycle bin --------------------------------------------------------
	mux.HandleFunc("GET /api/v1/trash", a.requireAuth(a.handleTrashList))
	mux.HandleFunc("POST /api/v1/trash/restore", a.requirePerm(store.PermDelete, a.handleTrashRestore))
	mux.HandleFunc("POST /api/v1/trash/delete", a.requirePerm(store.PermDelete, a.handleTrashDelete))
	mux.HandleFunc("POST /api/v1/trash/empty", a.requirePerm(store.PermDelete, a.handleTrashEmpty))

	// ---- shortcuts ----------------------------------------------------------
	mux.HandleFunc("GET /api/v1/favorites", a.requireAuth(a.handleFavorites))
	mux.HandleFunc("POST /api/v1/favorites", a.requireAuth(a.handleAddFavorite))
	mux.HandleFunc("DELETE /api/v1/favorites", a.requireAuth(a.handleRemoveFavorite))
	mux.HandleFunc("GET /api/v1/recent", a.requireAuth(a.handleRecent))

	// ---- public links -------------------------------------------------------
	mux.HandleFunc("GET /api/v1/shares", a.requirePerm(store.PermShare, a.handleListShares))
	mux.HandleFunc("POST /api/v1/shares", a.requirePerm(store.PermShare, a.handleCreateShare))
	mux.HandleFunc("PATCH /api/v1/shares/{id}", a.requirePerm(store.PermShare, a.handleUpdateShare))
	mux.HandleFunc("DELETE /api/v1/shares/{id}", a.requirePerm(store.PermShare, a.handleDeleteShare))

	// ---- accounts -----------------------------------------------------------
	mux.HandleFunc("GET /api/v1/users", a.requirePerm(store.PermUsers, a.handleListUsers))
	mux.HandleFunc("POST /api/v1/users", a.requirePerm(store.PermUsers, a.handleCreateUser))
	mux.HandleFunc("PATCH /api/v1/users/{id}", a.requirePerm(store.PermUsers, a.handleUpdateUser))
	mux.HandleFunc("DELETE /api/v1/users/{id}", a.requirePerm(store.PermUsers, a.handleDeleteUser))
	mux.HandleFunc("GET /api/v1/roles", a.requireAuth(a.handleRoles))

	// ---- operations ---------------------------------------------------------
	mux.HandleFunc("GET /api/v1/jobs", a.requireAuth(a.handleListJobs))
	mux.HandleFunc("GET /api/v1/jobs/{id}", a.requireAuth(a.handleGetJob))
	mux.HandleFunc("POST /api/v1/jobs/{id}/cancel", a.requireAuth(a.handleCancelJob))
	mux.HandleFunc("GET /api/v1/events", a.requireAuth(a.handleEvents))

	// ---- resumable uploads (tus 1.0.0) --------------------------------------
	mux.HandleFunc("OPTIONS /api/v1/tus", a.handleTusOptions)
	mux.HandleFunc("OPTIONS /api/v1/tus/{id}", a.handleTusOptions)
	mux.HandleFunc("POST /api/v1/tus", a.requirePerm(store.PermUpload, a.handleTusCreate))
	mux.HandleFunc("HEAD /api/v1/tus/{id}", a.requirePerm(store.PermUpload, a.handleTusHead))
	mux.HandleFunc("PATCH /api/v1/tus/{id}", a.requirePerm(store.PermUpload, a.handleTusPatch))
	mux.HandleFunc("DELETE /api/v1/tus/{id}", a.requirePerm(store.PermUpload, a.handleTusDelete))
	mux.HandleFunc("GET /api/v1/uploads", a.requirePerm(store.PermUpload, a.handleListUploads))

	// ---- system -------------------------------------------------------------
	mux.HandleFunc("GET /api/v1/dashboard", a.requireAuth(a.handleDashboard))
	mux.HandleFunc("GET /api/v1/system/info", a.requireAuth(a.handleSystemInfo))
	mux.HandleFunc("GET /api/v1/system/settings", a.requirePerm(store.PermSettings, a.handleGetSettings))
	mux.HandleFunc("PUT /api/v1/system/settings", a.requirePerm(store.PermSettings, a.handleSaveSettings))
	mux.HandleFunc("GET /api/v1/system/roots", a.requirePerm(store.PermSettings, a.handleListRootsAdmin))
	mux.HandleFunc("POST /api/v1/system/roots", a.requirePerm(store.PermSettings, a.handleAddRoot))
	mux.HandleFunc("PATCH /api/v1/system/roots/{id}", a.requirePerm(store.PermSettings, a.handleUpdateRoot))
	mux.HandleFunc("DELETE /api/v1/system/roots/{id}", a.requirePerm(store.PermSettings, a.handleDeleteRoot))
	mux.HandleFunc("GET /api/v1/system/browse", a.requirePerm(store.PermSettings, a.handleBrowseServer))
	mux.HandleFunc("GET /api/v1/system/audit", a.requirePerm(store.PermSettings, a.handleAudit))
	mux.HandleFunc("GET /api/v1/system/update/check", a.requireAdmin(a.handleUpdateCheck))
	mux.HandleFunc("POST /api/v1/system/update", a.requireAdmin(a.handleUpdateApply))
	mux.HandleFunc("POST /api/v1/system/domain", a.requireAdmin(a.handleSetDomain))

	// ---- public share access ------------------------------------------------
	mux.HandleFunc("GET /api/v1/public/{token}", a.handlePublicMeta)
	mux.HandleFunc("POST /api/v1/public/{token}/auth", a.handlePublicAuth)
	mux.HandleFunc("GET /api/v1/public/{token}/download", a.handlePublicDownload)
	mux.HandleFunc("GET /api/v1/public/{token}/download-zip", a.handlePublicZip)
	mux.HandleFunc("GET /api/v1/public/{token}/raw", a.handlePublicRaw)
	mux.HandleFunc("GET /api/v1/public/{token}/thumb", a.handlePublicThumb)
	mux.HandleFunc("OPTIONS /api/v1/public/{token}/tus", a.handleTusOptions)
	mux.HandleFunc("OPTIONS /api/v1/public/{token}/tus/{id}", a.handleTusOptions)
	mux.HandleFunc("POST /api/v1/public/{token}/tus", a.handlePublicTusCreate)
	mux.HandleFunc("HEAD /api/v1/public/{token}/tus/{id}", a.handlePublicTusHead)
	mux.HandleFunc("PATCH /api/v1/public/{token}/tus/{id}", a.handlePublicTusPatch)

	// ---- web application ----------------------------------------------------
	if a.Static != nil {
		mux.Handle("/", a.Static)
	}

	// ---- network drive ------------------------------------------------------
	// WebDAV lets Storix be mounted in Windows Explorer, the macOS Finder or a
	// Linux file manager. It authenticates with Basic credentials rather than
	// the session cookie, so it sits outside the CSRF guard by design.
	//
	// It is dispatched ahead of the mux rather than registered on it, because
	// ServeMux cleans a path before routing and would answer a name holding
	// ".." with a redirect out of the drive and into the web application. A
	// file manager mounted as a drive has to stay inside its own name space
	// and simply say the name is not there.
	dav := a.webdavHandler()
	var h http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/dav" || strings.HasPrefix(r.URL.Path, "/dav/") {
			dav.ServeHTTP(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
	h = a.setupGate(h)
	h = a.writeGuard(h)
	h = a.csrfGuard(h)
	h = a.authenticate(h)
	h = a.ipAllowlist(h)
	h = a.accessLog(h)
	h = a.securityHeaders(h)
	h = a.recoverer(h)
	return h
}
