$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [Text.Encoding]::UTF8
try {
    $request = [Console]::In.ReadToEnd() | ConvertFrom-Json
    $sid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
    $scheduler = New-Object -ComObject Schedule.Service
    $scheduler.Connect()
    $folder = $scheduler.GetFolder('\')
    $name = "KleiRewardHelper-$sid"
    if ($request.action -eq 'enable') {
        $task = $scheduler.NewTask(0)
        $task.RegistrationInfo.Description = 'Klei Points Auto-Claim'
        $task.Principal.UserId = $sid
        $task.Principal.LogonType = 3
        $task.Principal.RunLevel = 0
        $task.Settings.MultipleInstances = 2
        $task.Settings.DisallowStartIfOnBatteries = $false
        $task.Settings.StopIfGoingOnBatteries = $false
        $task.Settings.StartWhenAvailable = $true
        $task.Settings.ExecutionTimeLimit = 'PT1H'
        $clock = $task.Triggers.Create(1)
        $clock.StartBoundary = (Get-Date).AddSeconds(5).ToString('s')
        $clock.Repetition.Interval = 'PT1H'
        $logon = $task.Triggers.Create(9)
        $logon.UserId = $sid
        $exec = $task.Actions.Create(0)
        $exec.Path = $request.executable
        $exec.Arguments = $request.arguments
        $null = $folder.RegisterTaskDefinition($name, $task, 6, $sid, $null, 3)
    } else {
        try {
            $task = $folder.GetTask($name)
        } catch {
            if ($_.Exception.GetBaseException().HResult -ne -2147024894) { throw }
            $task = $null
        }
        if ($request.action -eq 'disable' -and $null -ne $task) {
            $folder.DeleteTask($name, 0)
        } elseif ($request.action -eq 'status') {
            if ($null -ne $task -and $task.Enabled) { 'enabled' } else { 'disabled' }
        }
    }
} catch {
    [Console]::Error.WriteLine($_.Exception.GetBaseException().Message)
    exit 1
}
