// Cloudflare Access login has to run inside the gateway tab, because Access ties the login to cookies set there.
function allowsGatewayNavigation(candidateUrl, allowedOrigin) {
  try {
    const url = new URL(candidateUrl);
    if (url.origin === allowedOrigin) return true;
    return url.protocol === "https:" && url.hostname.endsWith(".cloudflareaccess.com");
  } catch (_error) {
    return false;
  }
}

module.exports = { allowsGatewayNavigation };
