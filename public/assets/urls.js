function endpointUrl(url, pathname, location) {
  const target = new URL(url || location.href, location.href);
  const endpoint = new URL(pathname, location.origin);
  target.searchParams.forEach((value, key) => endpoint.searchParams.append(key, value));
  return endpoint;
}

export function sessionUrl(sessionPath, location = window.location) {
  const url = new URL("/", location.origin);
  const currentProject = new URLSearchParams(location.search).get("project");
  url.searchParams.set("session", sessionPath);
  if (currentProject) url.searchParams.set("project", currentProject);
  return `${url.pathname}${url.search}`;
}

export function sessionFragmentUrl(url, location = window.location) {
  return endpointUrl(url, "/session_fragment", location);
}

export function newSessionModalUrl(url, location = window.location) {
  return endpointUrl(url, "/new_session_modal", location);
}
