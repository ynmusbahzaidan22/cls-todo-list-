# Frontend page shell

`index.html` is the small HTML document shell. Its fixed include markers are expanded by the Go router from an explicit allowlist; the resulting page is served from `/`. Keep login, signup, dashboard, and section markup in the corresponding `frontend/pages/` and `frontend/sections/` partials. Partial paths are not public routes.
