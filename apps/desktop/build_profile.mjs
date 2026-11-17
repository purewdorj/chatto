/** Keep development installs separate from the signed release and its permissions. */
export function desktopBuildProfile(variant = 'release') {
  if (!['release', 'development'].includes(variant)) {
    throw new Error('CHATTO_DESKTOP_BUILD must be release or development.');
  }
  const development = variant === 'development';
  return {
    variant,
    // Setunari: our release build installs next to the official Chatto app with its own
    // name, storage, and permission identity.
    productName: development ? 'Chatto Dev' : 'Setunari Chat',
    // Keep existing sessions and preferences when the display name changes.
    userDataName: development ? 'Chatto Desktop Dev' : 'Setunari Chat Desktop',
    packageName: development ? 'chatto-desktop-dev' : 'setunari-chat-desktop',
    bundleIdentifier: development ? 'run.chatto.desktop.dev' : 'mn.setunari.chatto'
  };
}
