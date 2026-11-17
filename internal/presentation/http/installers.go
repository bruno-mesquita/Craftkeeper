package httpapi

import "net/http"

// minecraftVersions lists the game versions a loader can install, newest first.
func (a *API) minecraftVersions(w http.ResponseWriter, r *http.Request) {
	list, err := a.installers.MinecraftVersions(r.Context(), r.PathValue("loader"))
	if err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	if list == nil {
		list = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"versions": list})
}

// loaderVersions lists a loader's builds for one game release, newest first.
func (a *API) loaderVersions(w http.ResponseWriter, r *http.Request) {
	lv, err := a.installers.LoaderVersions(r.Context(), r.PathValue("loader"), r.PathValue("minecraft"))
	if err != nil {
		writeDomainError(w, a.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"loader_versions": lv.Versions, "recommended": lv.Recommended})
}
