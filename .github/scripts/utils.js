const fs = require('fs');

// /**
//  * Create a new comment on an issue or pull request.
//  * @param {*} github GitHub object reference.
//  * @param {*} context GitHub action context.
//  * @param {string} content Content of the comment.
//  */
// async function createComment(github, context, content) {
//   await github.issues.createComment({
//     owner: context.repo.owner,
//     repo: context.repo.repo,
//     issue_number: context.issue.number,
//     body: content,
//   });
// }


// /**
//  * 
//  * @param {*} github GitHub object reference.
//  * @param {*} context GitHub action context.
//  * @param {string} coverageData is colon separated key value pairs of the form component=coverage
//  * @param {number} threshold is the minimum coverage required to not issue a warning.
//  */
// export async function warnOnCertTestCoverage(github, context, coverageData, threshold) {
//     const coverages = coverageData.split(':');
//     var content = "";
//     coverages.forEach(coverage => {
//         // skip if empty
//         if (coverage.length === 0) { return; }
//         const [component, coverageValue] = coverage.split('=');
//         coverageValue = parseFloat(coverageValue);
//         if (coverageValue < threshold) {
//             content += `${component}: ${coverageValue}%\n`;
//         }
//     })
//     if (content.length > 0) {
//         const prefix = "Warning, the following components have a coverage below the threshold:\n";
//         content = prefix + content;
//         await createComment(github, context, content);
//     }
// }

async function calculateTotalCoveragePercentage(certTest_covFiles) {
  let totalNumerator = 0;
  let totalDenominator = 0;
  let finalPercentage = 0;
  let filenames = fs.readdirSync(certTest_covFiles);        
  const parseProviders = () => {
    return new Promise((accept, _) => {
      const promises = filenames.map(file => new Promise((resolve, _) => {
        fs.readFile(certTest_covFiles+ "/" + file, (err, data) => {
          if (err) throw err;
          let regex= /[^\(\}]+(?=\))/g;
          let getRatioInsideBraces= data.toString().match(regex);
          let parts = getRatioInsideBraces[0].split('/');
          console.log(parts);
          totalNumerator = +totalNumerator + +parts[0];
          totalDenominator = +totalDenominator + +parts[1];
          console.log(totalNumerator);
          console.log(totalDenominator);
          resolve();
        })
      }));
      accept(Promise.all(promises));
    });
  }
  (async () => {
      await parseProviders()
      finalPercentage = +totalNumerator / (+totalDenominator + 1) * 100;
      console.log("Total percentage is: " + finalPercentage + "%");
      return finalPercentage;
  })();
}

module.exports = {
  calculateTotalCoveragePercentage
};

// exports.calculateTotalCoveragePercentage = calculateTotalCoveragePercentage(certTest_covFiles);
// exports.otherMethod = function() {};