$ErrorActionPreference = "Stop"

$repo = "atinylittleshell/treehouse"
$installDir = "$env:LOCALAPPDATA\treehouse"

$arch = if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }

$release = Invoke-RestMethod -Uri "https://api.github.com/repos/$repo/releases/latest"
$version = $release.tag_name
$versionNum = $version.TrimStart("v")

$filename = "treehouse-v$versionNum-windows-$arch.zip"
$url = "https://github.com/$repo/releases/download/$version/$filename"

$tmpDir = New-TemporaryFile | ForEach-Object { Remove-Item $_; New-Item -ItemType Directory -Path $_ }

Write-Host "Downloading treehouse $version for windows/$arch..."
Invoke-WebRequest -Uri $url -OutFile "$tmpDir\$filename"
Expand-Archive -Path "$tmpDir\$filename" -DestinationPath $tmpDir -Force

New-Item -ItemType Directory -Path $installDir -Force | Out-Null
Move-Item -Path "$tmpDir\treehouse.exe" -Destination "$installDir\treehouse.exe" -Force

Remove-Item -Recurse -Force $tmpDir

# Add to PATH if not already there
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($userPath -notlike "*$installDir*") {
    [Environment]::SetEnvironmentVariable("Path", "$userPath;$installDir", "User")
    Write-Host "Added $installDir to user PATH. Restart your terminal for it to take effect."
}

Write-Host "treehouse $version installed to $installDir\treehouse.exe"
